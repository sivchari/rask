// Command spike-s2 measures how fast a minimal Linux guest boots to a Go
// PID 1 (rask-init) under Apple's Virtualization.framework, via the
// code-hex/vz Go bindings. See RESULTS.md for methodology and numbers.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
)

const bootMarker = "RASK-INIT-BOOT-COMPLETE"

// sample holds one boot's timings, both measured from t0 (just before
// vm.Start()).
type sample struct {
	t1 time.Duration // t0 -> first byte observed on the console
	t2 time.Duration // t0 -> rask-init's boot-complete marker line
}

func main() {
	runs := flag.Int("runs", 10, "number of boot iterations to measure")
	kernel := flag.String("kernel", "work/Image", "path to an uncompressed arm64 Linux kernel Image")
	initrd := flag.String("initrd", "work/initramfs.cpio", "path to the initramfs cpio archive")
	cpus := flag.Uint("cpus", 1, "number of vCPUs")
	memMiB := flag.Uint64("mem-mib", 1024, "guest memory in MiB")
	timeout := flag.Duration("timeout", 5*time.Second, "per-run timeout waiting for the boot marker")
	flag.Parse()

	kernelPath, err := filepath.Abs(*kernel)
	if err != nil {
		log.Fatalf("resolve kernel path: %v", err)
	}
	initrdPath, err := filepath.Abs(*initrd)
	if err != nil {
		log.Fatalf("resolve initrd path: %v", err)
	}

	samples := make([]sample, 0, *runs)
	for i := 1; i <= *runs; i++ {
		s, err := bootOnce(kernelPath, initrdPath, *cpus, *memMiB, *timeout)
		if err != nil {
			log.Fatalf("run %d: %v", i, err)
		}
		fmt.Printf("run %02d: t1(first console byte)=%-10s t2(init marker)=%s\n", i, s.t1, s.t2)
		samples = append(samples, s)
	}

	fmt.Println()
	report("t1 (vm.Start -> first console byte)", pick(samples, func(s sample) time.Duration { return s.t1 }))
	report("t2 (vm.Start -> rask-init marker)   ", pick(samples, func(s sample) time.Duration { return s.t2 }))
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

// bootOnce creates a fresh VM, boots it, and returns timings measured from
// just before vm.Start(). The VM is force-stopped before returning.
func bootOnce(kernelPath, initrdPath string, cpus uint, memMiB uint64, timeout time.Duration) (sample, error) {
	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithCommandLine("console=hvc0 reboot=t panic=-1"),
		vz.WithInitrd(initrdPath),
	)
	if err != nil {
		return sample{}, fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cpus, memMiB*1024*1024)
	if err != nil {
		return sample{}, fmt.Errorf("vm config: %w", err)
	}

	consoleInR, consoleInW, err := os.Pipe()
	if err != nil {
		return sample{}, fmt.Errorf("console in pipe: %w", err)
	}
	defer consoleInR.Close()
	defer consoleInW.Close()

	consoleOutR, consoleOutW, err := os.Pipe()
	if err != nil {
		return sample{}, fmt.Errorf("console out pipe: %w", err)
	}
	defer consoleOutR.Close()
	defer consoleOutW.Close()

	attachment, err := vz.NewFileHandleSerialPortAttachment(consoleInR, consoleOutW)
	if err != nil {
		return sample{}, fmt.Errorf("serial attachment: %w", err)
	}
	serialConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return sample{}, fmt.Errorf("serial config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return sample{}, fmt.Errorf("entropy config: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyConfig})

	if ok, err := config.Validate(); !ok || err != nil {
		return sample{}, fmt.Errorf("invalid config: valid=%v err=%w", ok, err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return sample{}, fmt.Errorf("new vm: %w", err)
	}

	firstByte := make(chan time.Time, 1)
	markerSeen := make(chan time.Time, 1)
	readDone := make(chan error, 1)

	go watchConsole(consoleOutR, firstByte, markerSeen, readDone)

	t0 := time.Now()
	if err := vm.Start(); err != nil {
		return sample{}, fmt.Errorf("vm start: %w", err)
	}
	defer stopVM(vm)

	deadline := time.After(timeout)

	var s sample
	select {
	case t1 := <-firstByte:
		s.t1 = t1.Sub(t0)
	case <-deadline:
		return sample{}, fmt.Errorf("timeout waiting for first console byte")
	}

	select {
	case t2 := <-markerSeen:
		s.t2 = t2.Sub(t0)
	case err := <-readDone:
		return sample{}, fmt.Errorf("console closed before boot marker: %w", err)
	case <-deadline:
		return sample{}, fmt.Errorf("timeout waiting for boot marker")
	}

	return s, nil
}

// watchConsole reads console lines, reporting the time of the first byte
// seen and the time the boot marker line is seen.
func watchConsole(r *os.File, firstByte, markerSeen chan<- time.Time, done chan<- error) {
	reader := bufio.NewReader(r)
	gotFirst := false
	for {
		line, err := reader.ReadString('\n')
		now := time.Now()
		if len(line) > 0 && !gotFirst {
			gotFirst = true
			firstByte <- now
		}
		if strings.Contains(line, bootMarker) {
			markerSeen <- now
			done <- nil
			return
		}
		if err != nil {
			done <- err
			return
		}
	}
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
