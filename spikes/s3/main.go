// Command spike-s3 boots the S3 guest (see init/) under Apple's
// Virtualization.framework and measures how long it takes to mount
// Rosetta, come up on the network, start containerd, pull a real
// amd64-only image, and run it -- proving an arm64 vz guest can run
// amd64 containers via Rosetta. See RESULTS.md for methodology and numbers.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
)

func main() {
	runs := flag.Int("runs", 3, "number of end-to-end boot iterations to measure")
	kernel := flag.String("kernel", "work/Image", "path to an uncompressed arm64 Linux kernel Image")
	initrd := flag.String("initrd", "work/initramfs.cpio", "path to the initramfs cpio archive")
	cpus := flag.Uint("cpus", 2, "number of vCPUs")
	memMiB := flag.Uint64("mem-mib", 2048, "guest memory in MiB")
	timeout := flag.Duration("timeout", 90*time.Second, "per-run timeout waiting for RASK-S3-ALL-DONE")
	flag.Parse()

	kernelPath, err := filepath.Abs(*kernel)
	if err != nil {
		log.Fatalf("resolve kernel path: %v", err)
	}
	initrdPath, err := filepath.Abs(*initrd)
	if err != nil {
		log.Fatalf("resolve initrd path: %v", err)
	}

	rosettaAvail := vz.LinuxRosettaDirectoryShareAvailability()
	fmt.Printf("host Rosetta availability: %s\n", rosettaAvail)
	if rosettaAvail != vz.LinuxRosettaAvailabilityInstalled {
		log.Fatalf("Rosetta is not installed/available on this host (%s); stopping rather than "+
			"faking it -- install it with `softwareupdate --install-rosetta --agree-to-license` "+
			"or vz.LinuxRosettaDirectoryShareInstallRosetta(), then re-run", rosettaAvail)
	}

	results := make([]runResult, 0, *runs)
	for i := 1; i <= *runs; i++ {
		fmt.Printf("\n=== run %d/%d ===\n", i, *runs)
		r, err := bootOnce(kernelPath, initrdPath, *cpus, *memMiB, *timeout)
		if err != nil {
			log.Fatalf("run %d: %v", i, err)
		}
		results = append(results, r)
	}

	printSummary(results)
}

// runResult holds the per-stage durations parsed from one guest's console
// markers, plus the observed `uname -m` output from inside the amd64
// container (the spike's pass/fail signal).
type runResult struct {
	mounts       time.Duration
	netUp        time.Duration
	containerd   time.Duration
	pull         time.Duration
	run          time.Duration
	allDone      time.Duration
	unameOutput  string
	rosettaMount bool
	binfmtOK     bool
}

func bootOnce(kernelPath, initrdPath string, cpus uint, memMiB uint64, timeout time.Duration) (runResult, error) {
	// puipui-linux has no RTC, so the guest clock starts at the Unix
	// epoch, which breaks TLS certificate validation for the registry
	// pull. Hand the guest our current wall-clock time via a kernel
	// cmdline param; init applies it with clock_settime very early
	// (init/clock.go). The gap between reading it here and vm.Start()
	// below is object setup only (no I/O), so the resulting skew is
	// negligible next to certificate validity windows.
	cmdline := fmt.Sprintf("console=hvc0 reboot=t panic=-1 rask.boottime=%d", time.Now().UnixNano())
	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithCommandLine(cmdline),
		vz.WithInitrd(initrdPath),
	)
	if err != nil {
		return runResult{}, fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cpus, memMiB*1024*1024)
	if err != nil {
		return runResult{}, fmt.Errorf("vm config: %w", err)
	}

	consoleInR, consoleInW, err := os.Pipe()
	if err != nil {
		return runResult{}, fmt.Errorf("console in pipe: %w", err)
	}
	defer consoleInR.Close()
	defer consoleInW.Close()

	consoleOutR, consoleOutW, err := os.Pipe()
	if err != nil {
		return runResult{}, fmt.Errorf("console out pipe: %w", err)
	}
	defer consoleOutR.Close()
	defer consoleOutW.Close()

	attachment, err := vz.NewFileHandleSerialPortAttachment(consoleInR, consoleOutW)
	if err != nil {
		return runResult{}, fmt.Errorf("serial attachment: %w", err)
	}
	serialConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return runResult{}, fmt.Errorf("serial config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return runResult{}, fmt.Errorf("entropy config: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyConfig})

	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return runResult{}, fmt.Errorf("nat attachment: %w", err)
	}
	netConfig, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return runResult{}, fmt.Errorf("network config: %w", err)
	}
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netConfig})

	rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return runResult{}, fmt.Errorf("rosetta directory share: %w", err)
	}
	rosettaFSConfig, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
	if err != nil {
		return runResult{}, fmt.Errorf("rosetta fs config: %w", err)
	}
	rosettaFSConfig.SetDirectoryShare(rosettaShare)
	config.SetDirectorySharingDevicesVirtualMachineConfiguration(
		[]vz.DirectorySharingDeviceConfiguration{rosettaFSConfig},
	)

	if ok, err := config.Validate(); !ok || err != nil {
		return runResult{}, fmt.Errorf("invalid config: valid=%v err=%w", ok, err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return runResult{}, fmt.Errorf("new vm: %w", err)
	}

	t0 := time.Now()
	lines := make(chan string, 256)
	done := make(chan error, 1)
	go watchConsole(consoleOutR, lines, done)

	if err := vm.Start(); err != nil {
		return runResult{}, fmt.Errorf("vm start: %w", err)
	}
	defer stopVM(vm)

	return collectResult(t0, lines, done, timeout)
}

// collectResult is the sole consumer of the console line channel: it prints
// every line live (so the run is observable while it happens) and extracts
// the RASK-S3-* marker fields into a runResult. It stops as soon as
// ALL-DONE or a *-FAILED marker is seen, or timeout elapses.
func collectResult(t0 time.Time, lines <-chan string, readDone <-chan error, timeout time.Duration) (runResult, error) {
	var r runResult
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return r, fmt.Errorf("console closed before RASK-S3-ALL-DONE")
			}
			fmt.Printf("  guest: %s\n", line)
			parseMarker(line, &r)
			if strings.Contains(line, "RASK-S3-ALL-DONE") {
				r.allDone = time.Since(t0)
				return r, nil
			}
			if strings.Contains(line, "-FAILED") {
				return r, fmt.Errorf("guest reported failure: %s", line)
			}
		case err := <-readDone:
			return r, fmt.Errorf("console closed: %w", err)
		case <-deadline:
			return r, fmt.Errorf("timeout after %s waiting for RASK-S3-ALL-DONE", timeout)
		}
	}
}

func parseMarker(line string, r *runResult) {
	switch {
	case strings.Contains(line, "RASK-S3-MOUNTS-DONE"):
		r.mounts = parseDurationField(line, "t=")
	case strings.Contains(line, "RASK-S3-ROSETTA-MOUNTED"):
		r.rosettaMount = true
	case strings.Contains(line, "RASK-S3-BINFMT-REGISTERED"):
		r.binfmtOK = true
	case strings.Contains(line, "RASK-S3-NET-UP"):
		r.netUp = parseDurationField(line, "t=")
	case strings.Contains(line, "RASK-S3-CONTAINERD-READY"):
		r.containerd = parseDurationField(line, "t=")
	case strings.Contains(line, "RASK-S3-PULL-DONE"):
		r.pull = parseDurationField(line, "t=")
	case strings.Contains(line, "RASK-S3-RUN-DONE"):
		r.run = parseDurationField(line, "t=")
		r.unameOutput = parseStringField(line, "uname=")
	}
}

func parseDurationField(line, key string) time.Duration {
	v := parseStringField(line, key)
	d, _ := time.ParseDuration(v)
	return d
}

// parseStringField extracts the value of a "key=value" token from a
// space-separated marker line, where key includes the trailing "=".
func parseStringField(line, key string) string {
	idx := strings.Index(line, key)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(key):]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		return rest[:sp]
	}
	return rest
}

func watchConsole(r *os.File, lines chan<- string, done chan<- error) {
	defer close(lines)
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lines <- strings.TrimRight(line, "\r\n")
		}
		if err != nil {
			done <- err
			return
		}
	}
}

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

func printSummary(results []runResult) {
	fmt.Println("\n=== summary ===")
	for i, r := range results {
		fmt.Printf("run %02d: mounts=%-8s net=%-9s containerd=%-9s pull=%-9s run=%-9s all=%-9s rosetta=%v binfmt=%v uname=%q\n",
			i+1, r.mounts, r.netUp, r.containerd, r.pull, r.run, r.allDone, r.rosettaMount, r.binfmtOK, r.unameOutput)
	}

	allPass := true
	for _, r := range results {
		if r.unameOutput != "x86_64" {
			allPass = false
		}
	}
	if allPass {
		fmt.Println("\nPASS: every run's `uname -m` inside the amd64-only image reported x86_64")
	} else {
		fmt.Println("\nFAIL: at least one run did not report x86_64 from inside the container")
	}
}
