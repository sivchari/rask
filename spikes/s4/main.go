// Command spike-s4 measures rask's host<->guest networking path: a vz
// microVM whose only network device is wired, via a connected unixgram
// socketpair, straight into gvisor-tap-vsock's virtualnetwork stack running
// in-process on the host. A host TCP port is forwarded through that stack
// to a stand-in "apiserver" HTTP server in the guest (spikes/s4/init),
// proving the pattern rask will use to expose the real kube-apiserver.
// See RESULTS.md for methodology, wiring, and numbers.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/sirupsen/logrus"
)

const (
	netReadyMarker    = "RASK-INIT-NET-READY"
	netFailedMarker   = "RASK-INIT-NET-FAILED"
	outboundMarker    = "RASK-OUTBOUND-RESULT"
	guestAddr         = "192.168.127.2:6443"
	subnet            = "192.168.127.0/24"
	gatewayIP         = "192.168.127.1"
	gatewayMacAddress = "5a:94:ef:e4:0c:dd"
	expectedHTTPBody  = "rask-spike-s4-guest-ok\n"
)

// sample holds one boot's timings and outcomes, measured from t0 (just
// before vm.Start()).
type sample struct {
	httpReady    time.Duration // t0 -> first successful HTTP response through the host->guest forward
	netReady     time.Duration // t0 -> guest console reports eth0/routes configured (informational)
	outboundLine string        // guest's outbound NAT/DNS probe result line, captured only when requested
}

func main() {
	runs := flag.Int("runs", 10, "number of boot iterations to measure for the boot->HTTP-through-forward latency")
	kernel := flag.String("kernel", "work/Image", "path to an uncompressed arm64 Linux kernel Image")
	initrd := flag.String("initrd", "work/initramfs.cpio", "path to the initramfs cpio archive")
	cpus := flag.Uint("cpus", 1, "number of vCPUs")
	memMiB := flag.Uint64("mem-mib", 1024, "guest memory in MiB")
	httpTimeout := flag.Duration("http-timeout", 5*time.Second, "per-run timeout waiting for the first HTTP response through the forward")
	outboundTimeout := flag.Duration("outbound-timeout", 20*time.Second, "timeout waiting for the guest's outbound NAT/DNS probe result (checked once)")
	verbose := flag.Bool("v", false, "echo raw guest console lines")
	flag.Parse()

	kernelPath, err := filepath.Abs(*kernel)
	if err != nil {
		log.Fatalf("resolve kernel path: %v", err)
	}
	initrdPath, err := filepath.Abs(*initrd)
	if err != nil {
		log.Fatalf("resolve initrd path: %v", err)
	}

	// gvisor-tap-vsock logs an "expected on cleanup" error via the package-
	// level logrus logger every time we intentionally close the socketpair
	// between runs (see bootOnce). Silence it so the benchmark output stays
	// readable; real wiring failures still surface through our own errors.
	logrus.SetLevel(logrus.FatalLevel)

	cfg := bootConfig{
		kernelPath:      kernelPath,
		initrdPath:      initrdPath,
		cpus:            *cpus,
		memMiB:          *memMiB,
		httpTimeout:     *httpTimeout,
		outboundTimeout: *outboundTimeout,
		verbose:         *verbose,
	}

	samples := make([]sample, 0, *runs)
	var outboundResult string
	for i := 1; i <= *runs; i++ {
		captureOutbound := i == 1 // the NAT/DNS answer is a yes/no about the substrate, not a per-run latency; only pay its cost once
		s, err := bootOnce(cfg, captureOutbound)
		if err != nil {
			log.Fatalf("run %d: %v", i, err)
		}
		fmt.Printf("run %02d: netReady=%-10s httpReady=%s\n", i, s.netReady, s.httpReady)
		if s.outboundLine != "" {
			outboundResult = s.outboundLine
			fmt.Printf("  outbound probe: %s\n", outboundResult)
		}
		samples = append(samples, s)
	}

	fmt.Println()
	report("httpReady (vm.Start -> first HTTP response through forward)", pick(samples, func(s sample) time.Duration { return s.httpReady }))
	report("netReady  (vm.Start -> guest console net-ready marker)     ", pick(samples, func(s sample) time.Duration { return s.netReady }))
	fmt.Printf("outbound NAT/DNS probe (run 1): %s\n", outboundResult)
}

func pick(samples []sample, f func(sample) time.Duration) []time.Duration {
	out := make([]time.Duration, len(samples))
	for i, s := range samples {
		out[i] = f(s)
	}
	return out
}

func report(label string, durations []time.Duration) {
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	fmt.Printf("%s: p50=%-10s p95=%-10s min=%-10s max=%-10s (n=%d)\n",
		label, percentile(sorted, 0.50), percentile(sorted, 0.95), sorted[0], sorted[len(sorted)-1], len(sorted))
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type bootConfig struct {
	kernelPath      string
	initrdPath      string
	cpus            uint
	memMiB          uint64
	httpTimeout     time.Duration
	outboundTimeout time.Duration
	verbose         bool
}

// bootOnce creates a fresh gvisor-tap-vsock virtual network and a fresh VM
// wired to it over a unixgram socketpair, boots the VM, forwards a host
// ephemeral port to the guest's stand-in apiserver, and measures the time
// from vm.Start() to the first successful HTTP response through that
// forward. The VM is force-stopped before returning.
func bootOnce(cfg bootConfig, captureOutbound bool) (sample, error) {
	hostPort, err := freePort()
	if err != nil {
		return sample{}, fmt.Errorf("pick free host port: %w", err)
	}

	netConfig := &types.Configuration{
		MTU:               1500,
		Subnet:            subnet,
		GatewayIP:         gatewayIP,
		GatewayMacAddress: gatewayMacAddress,
		Forwards: map[string]string{
			fmt.Sprintf("127.0.0.1:%d", hostPort): guestAddr,
		},
	}
	vn, err := virtualnetwork.New(netConfig)
	if err != nil {
		return sample{}, fmt.Errorf("virtualnetwork.New: %w", err)
	}

	guestFile, hostConn, err := connectedDatagramPair()
	if err != nil {
		return sample{}, fmt.Errorf("datagram socketpair: %w", err)
	}
	defer guestFile.Close()
	defer hostConn.Close()

	acceptCtx, cancelAccept := context.WithCancel(context.Background())
	defer cancelAccept()
	go func() {
		if err := vn.AcceptVfkit(acceptCtx, hostConn); err != nil && acceptCtx.Err() == nil {
			log.Printf("AcceptVfkit: %v", err)
		}
	}()

	vm, err := newVirtualMachine(cfg, guestFile)
	if err != nil {
		return sample{}, err
	}
	defer vm.Close()

	netReadyAt := make(chan time.Time, 1)
	netFailed := make(chan string, 1)
	outboundLine := make(chan string, 1)
	consoleDone := make(chan error, 1)
	go watchConsole(vm.console, netReadyAt, netFailed, outboundLine, consoleDone, cfg.verbose)

	t0 := time.Now()
	if err := vm.vm.Start(); err != nil {
		return sample{}, fmt.Errorf("vm start: %w", err)
	}
	defer stopVM(vm.vm)

	var s sample

	httpDone := make(chan time.Duration, 1)
	httpErr := make(chan error, 1)
	go func() {
		d, err := waitForHTTPReady(hostPort, t0, cfg.httpTimeout)
		if err != nil {
			httpErr <- err
			return
		}
		httpDone <- d
	}()

	select {
	case s.httpReady = <-httpDone:
	case err := <-httpErr:
		return sample{}, fmt.Errorf("waiting for HTTP through forward: %w", err)
	case line := <-netFailed:
		return sample{}, fmt.Errorf("guest reported network setup failure: %s", line)
	case <-time.After(cfg.httpTimeout + 2*time.Second):
		return sample{}, fmt.Errorf("timeout waiting for HTTP through forward")
	}

	select {
	case t := <-netReadyAt:
		s.netReady = t.Sub(t0)
	default:
	}

	if captureOutbound {
		select {
		case line := <-outboundLine:
			s.outboundLine = line
		case <-time.After(cfg.outboundTimeout):
			s.outboundLine = "timeout waiting for outbound probe result"
		case <-consoleDone:
			s.outboundLine = "console closed before outbound probe result"
		}
	}

	return s, nil
}

// freePort binds an ephemeral TCP port on 127.0.0.1, closes it, and returns
// the port number for gvisor-tap-vsock's forwarder to bind next. This is a
// bind-then-close-then-reuse pattern with an inherent (and here acceptable)
// TOCTOU race against other local processes.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// connectedDatagramPair creates a SOCK_DGRAM AF_UNIX socketpair. The first
// returned file is handed to vz as the guest side of the virtio-net device;
// the second is wrapped as a net.Conn and handed to gvisor-tap-vsock's
// AcceptVfkit as the host side. Ethernet frames written to one end appear
// as whole datagrams on a Read of the other, matching both vz's
// FileHandleNetworkDeviceAttachment contract and gvisor-tap-vsock's vfkit
// wire protocol (bare L2 frames over SOCK_DGRAM, no length framing).
func connectedDatagramPair() (guestFile *os.File, hostConn net.Conn, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("socketpair: %w", err)
	}
	guestFile = os.NewFile(uintptr(fds[0]), "vz-guest-net")
	hostFile := os.NewFile(uintptr(fds[1]), "gvisor-host-net")
	defer hostFile.Close()

	hostConn, err = net.FileConn(hostFile)
	if err != nil {
		guestFile.Close()
		return nil, nil, fmt.Errorf("net.FileConn: %w", err)
	}
	return guestFile, hostConn, nil
}

type virtualMachine struct {
	vm      *vz.VirtualMachine
	console *os.File
	closers []io.Closer
}

// Close releases the console pipe file descriptors backing this VM's
// serial port attachment. It does not touch the guest network file, which
// bootOnce owns and closes itself.
func (v *virtualMachine) Close() {
	for _, c := range v.closers {
		_ = c.Close()
	}
}

// newVirtualMachine builds the vz VM configuration: the same
// VZLinuxBootLoader + serial console + entropy device wiring spike S2
// validated, plus a VirtioNetworkDeviceConfiguration attached to
// guestNetFile (the guest side of the socketpair connected to
// gvisor-tap-vsock).
func newVirtualMachine(cfg bootConfig, guestNetFile *os.File) (*virtualMachine, error) {
	bootLoader, err := vz.NewLinuxBootLoader(
		cfg.kernelPath,
		vz.WithCommandLine("console=hvc0 reboot=t panic=-1"),
		vz.WithInitrd(cfg.initrdPath),
	)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cfg.cpus, cfg.memMiB*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("vm config: %w", err)
	}

	var closers []io.Closer

	consoleInR, consoleInW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("console in pipe: %w", err)
	}
	closers = append(closers, consoleInR, consoleInW)

	consoleOutR, consoleOutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("console out pipe: %w", err)
	}
	closers = append(closers, consoleOutR, consoleOutW)

	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(consoleInR, consoleOutW)
	if err != nil {
		return nil, fmt.Errorf("serial attachment: %w", err)
	}
	serialConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return nil, fmt.Errorf("serial config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy config: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyConfig})

	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
	if err != nil {
		return nil, fmt.Errorf("mac address: %w", err)
	}
	netAttachment, err := vz.NewFileHandleNetworkDeviceAttachment(guestNetFile)
	if err != nil {
		return nil, fmt.Errorf("network attachment: %w", err)
	}
	netDeviceConfig, err := vz.NewVirtioNetworkDeviceConfiguration(netAttachment)
	if err != nil {
		return nil, fmt.Errorf("network device config: %w", err)
	}
	netDeviceConfig.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDeviceConfig})

	if ok, err := config.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("invalid config: valid=%v err=%w", ok, err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}

	return &virtualMachine{vm: vm, console: consoleOutR, closers: closers}, nil
}

// watchConsole reads guest console lines, reporting the net-ready marker
// time, a net-failed line (if any), and the first outbound probe result
// line it sees.
func watchConsole(r *os.File, netReadyAt chan<- time.Time, netFailed, outboundLine chan<- string, done chan<- error, verbose bool) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		now := time.Now()
		trimmed := strings.TrimRight(line, "\r\n")
		if verbose && trimmed != "" {
			fmt.Println("guest:", trimmed)
		}
		switch {
		case strings.Contains(line, netReadyMarker):
			select {
			case netReadyAt <- now:
			default:
			}
		case strings.Contains(line, netFailedMarker):
			select {
			case netFailed <- trimmed:
			default:
			}
		case strings.Contains(line, outboundMarker):
			select {
			case outboundLine <- trimmed:
			default:
			}
		}
		if err != nil {
			done <- err
			return
		}
	}
}

// waitForHTTPReady polls the host's forwarded port until the stand-in
// apiserver responds with the expected body, returning the elapsed time
// since t0.
func waitForHTTPReady(hostPort int, t0 time.Time, timeout time.Duration) (time.Duration, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(5 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err == nil && string(body) == expectedHTTPBody {
			return time.Since(t0), nil
		}
		lastErr = fmt.Errorf("unexpected response: status=%d body=%q err=%v", resp.StatusCode, body, err)
		time.Sleep(5 * time.Millisecond)
	}
	return 0, fmt.Errorf("last error: %w", lastErr)
}

// stopVM force-stops the VM and waits briefly for it to reach a terminal
// state so the next iteration starts clean.
func stopVM(vm *vz.VirtualMachine) {
	if vm.CanStop() {
		_ = vm.Stop()
	} else {
		_, _ = vm.RequestStop()
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case st := <-vm.StateChangedNotify():
			if st == vz.VirtualMachineStateStopped || st == vz.VirtualMachineStateError {
				return
			}
		case <-deadline:
			return
		}
	}
}
