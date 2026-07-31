//go:build darwin

package vz

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cvz "github.com/Code-Hex/vz/v3"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
)

// RunVMHost is the entry point for the "rask __vm-host" hidden CLI
// subcommand (cmd/rask), which Runtime.Start spawns as a detached child
// process (plan-m0-spikes.md's "daemonless, pidfile in the cluster dir"
// VM lifecycle decision): the VM and its gvisor-tap-vsock network live in
// this process, not in the "rask create" invocation that started it, so
// the cluster keeps running after that invocation exits.
//
// It acquires the host-wide VM lock and checks free memory first (both
// added after a real host instability incident during E2E testing on this
// exact machine — see lock.go and memcheck.go), then builds the network
// and writes vmState (PID + forwarded ports) as soon as it exists — before
// the VM even boots — so Start (a separate, short-lived process) can find
// the forwarded ports without guessing. Once the VM starts, a watchdog
// (watchdog.go) independently bounds how long it waits for the guest to
// report healthy, stopping the VM and exiting on its own if that never
// happens — not relying solely on the "rask create" process (which might
// itself die without ever sending a Stop) to bound this VM's lifetime.
func RunVMHost(ctx context.Context, homeDir, name string) error {
	lock, err := acquireVMLock(homeDir)
	if err != nil {
		return err
	}
	defer lock.Release()

	if err := checkFreeMemory(defaultMemoryMiB); err != nil {
		return err
	}

	dataDir := filepath.Join(cluster.Dir(homeDir, name), "data")
	diskPath := filepath.Join(dataDir, "disk.img")
	logPath := filepath.Join(dataDir, "console.log")
	idPath := filepath.Join(cluster.Dir(homeDir, name), machineIdentifierFileName)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", dataDir, err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("vz: opening console log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	cache := components.NewCache(filepath.Join(homeDir, "cache"))

	kernel, err := cache.EnsureGuestKernel(ctx)
	if err != nil {
		return fmt.Errorf("vz: resolving guest kernel: %w", err)
	}

	initramfsPath, err := buildTemplateInitramfs(ctx, cache)
	if err != nil {
		return err
	}

	// Overlay any files Runtime.Start staged (StartOptions.PrebootFiles)
	// onto the shared template initramfs as a second, per-cluster cpio
	// archive, so they land in the guest filesystem before rask-init (and
	// everything it launches) ever runs. Skipped entirely — reusing the
	// shared template path unmodified — when nothing was staged, the
	// overwhelmingly common case.
	prebootCpio, err := buildPrebootCpio(dataDir)
	if err != nil {
		return err
	}

	if len(prebootCpio) > 0 {
		combinedPath := filepath.Join(dataDir, "initramfs-combined.cpio")
		if err := concatInitramfs(combinedPath, initramfsPath, prebootCpio); err != nil {
			return err
		}

		initramfsPath = combinedPath
	}

	if err := createDataDisk(diskPath, defaultDataDiskGB); err != nil {
		return err
	}

	machineID, err := loadOrCreateMachineIdentifier(idPath)
	if err != nil {
		return err
	}

	net, err := newClusterNetwork()
	if err != nil {
		return fmt.Errorf("vz: creating cluster network: %w", err)
	}
	defer net.Close()

	statePath := vmStatePath(homeDir, name)
	if err := writeVMState(statePath, vmState{PID: os.Getpid(), HostPort: net.HostPort(), AgentHostPort: net.AgentHostPort()}); err != nil {
		return err
	}

	cfg := vmConfig{
		kernelImagePath:  kernel.ImagePath,
		initramfsPath:    initramfsPath,
		diskPath:         diskPath,
		machineID:        machineID,
		clusterName:      name,
		bootTimeUnixNano: time.Now().UnixNano(),
		net:              net,
	}

	vmm, console, err := buildVirtualMachineConfiguration(cfg)
	if err != nil {
		return err
	}
	defer console.Close()

	go logConsoleLines(console, logFile)

	vm, err := cvz.NewVirtualMachine(vmm)
	if err != nil {
		return fmt.Errorf("vz: creating virtual machine: %w", err)
	}

	if err := vm.Start(); err != nil {
		return fmt.Errorf("vz: starting virtual machine: %w", err)
	}

	defer stopVM(vm)

	// runCtx is what the rest of this function waits on: canceled either
	// by ctx itself (external SIGTERM, via cmd/rask/vmhost_darwin.go —
	// the normal Stop path) or by the boot watchdog below (the guest never
	// reported healthy in time). watchdogFailed distinguishes the two so
	// the right thing gets returned/logged in each case.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watchdogFailed := make(chan struct{}, 1)

	go runBootWatchdog(runCtx, cancel, net.AgentHostPort(), bootWatchdogTimeout, watchdogFailed)

	select {
	case <-runCtx.Done():
		return watchdogResult(watchdogFailed)
	case st := <-vm.StateChangedNotify():
		if st == cvz.VirtualMachineStateStopped || st == cvz.VirtualMachineStateError {
			return fmt.Errorf("vz: virtual machine unexpectedly reached state %v", st)
		}

		// Any other transition (e.g. Running) is not terminal; keep
		// waiting for either runCtx to be done or a terminal state.
		return waitForTerminalOrCancel(runCtx, vm, watchdogFailed)
	}
}

func waitForTerminalOrCancel(ctx context.Context, vm *cvz.VirtualMachine, watchdogFailed <-chan struct{}) error {
	for {
		select {
		case <-ctx.Done():
			return watchdogResult(watchdogFailed)
		case st := <-vm.StateChangedNotify():
			if st == cvz.VirtualMachineStateStopped || st == cvz.VirtualMachineStateError {
				return fmt.Errorf("vz: virtual machine unexpectedly reached state %v", st)
			}
		}
	}
}

// watchdogResult reports why runCtx ended: nil for a normal (external)
// stop, or a descriptive error if the boot watchdog fired.
func watchdogResult(watchdogFailed <-chan struct{}) error {
	select {
	case <-watchdogFailed:
		return fmt.Errorf("vz: guest did not report healthy within %s; stopped the VM", bootWatchdogTimeout)
	default:
		return nil
	}
}

// logConsoleLines writes every guest console line to w, timestamped, until
// the console closes. Runs for the lifetime of the VM host process.
func logConsoleLines(console *consolePipes, w *os.File) {
	for line := range console.Lines() {
		_, _ = fmt.Fprintf(w, "%s %s\n", time.Now().Format(time.RFC3339Nano), line)
	}
}
