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
// exact machine — see lock.go and memcheck.go). This is the lock's actual
// enforcement; Runtime.Start also does its own non-blocking peekVMLock
// check before ever spawning this process, purely so a doomed "rask
// create" fails in milliseconds with a precise, holder-naming error
// instead of only learning about the conflict once this process exits.
// Then it builds the network and writes vmState (PID + forwarded ports) as
// soon as it exists — before the VM even boots — so Start (a separate,
// short-lived process) can find the forwarded ports without guessing.
// Once the VM starts, a watchdog
// (watchdog.go) independently bounds how long it waits for the guest to
// report healthy, stopping the VM and exiting on its own if that never
// happens — not relying solely on the "rask create" process (which might
// itself die without ever sending a Stop) to bound this VM's lifetime.
func RunVMHost(ctx context.Context, homeDir, name string) error {
	lock, err := acquireVMLock(homeDir, name)
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

	// runCtx is what the rest of this function (and both background
	// goroutines below, via runRecoverable) waits on: canceled either by
	// ctx itself (external SIGTERM, via cmd/rask/vmhost_darwin.go — the
	// normal Stop path), by the boot watchdog (the guest never reported
	// healthy in time), or by a recovered panic in either background
	// goroutine. watchdogFailed distinguishes the watchdog case from the
	// other two so the right thing gets returned/logged.
	//
	// Declared before either goroutine starts (rather than only once the
	// VM itself exists, as a previous version did) so runRecoverable
	// always has a live cancel to call.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go runRecoverable("console logger", cancel, func() { logConsoleLines(console, logFile) })

	vm, err := cvz.NewVirtualMachine(vmm)
	if err != nil {
		return fmt.Errorf("vz: creating virtual machine: %w", err)
	}

	if err := vm.Start(); err != nil {
		return fmt.Errorf("vz: starting virtual machine: %w", err)
	}

	defer stopVM(vm)

	watchdogFailed := make(chan struct{}, 1)

	go runRecoverable("boot watchdog", cancel, func() {
		runBootWatchdog(runCtx, cancel, net.AgentHostPort(), bootWatchdogTimeout, watchdogFailed)
	})

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

// runRecoverable runs fn in the caller's goroutine (call as
// `go runRecoverable(...)`) with a recover in place, instead of letting an
// unhandled panic in fn crash the whole vm-host process outright.
//
// This matters specifically because of where fn runs: an unrecovered
// panic in any goroutine terminates the entire Go program immediately,
// running only *that* goroutine's own deferred calls — RunVMHost's
// deferred stopVM/console.Close/net.Close/lock.Release calls belong to
// the main goroutine, not to logConsoleLines' or runBootWatchdog's, so
// they would never run. That would leave the actual
// Virtualization.framework VM process running as an orphaned
// launchd/XPC child (it is not a child of this process — see the
// package doc comment) with nothing left to stop it: the exact shape of
// a leaked-VM incident investigated during this session (a
// com.apple.Virtualization.VirtualMachine process still running 23h
// later at 40%+ CPU, unnoticed, after its vm-host had already exited).
//
// Recovering here and calling cancel instead routes the failure back
// through RunVMHost's normal runCtx.Done() path in the main goroutine, so
// its deferred cleanup — including stopVM — still runs. label is included
// in the logged message purely to identify which goroutine panicked.
func runRecoverable(label string, cancel context.CancelFunc, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "vz: recovered panic in %s goroutine: %v\n", label, r)
			cancel()
		}
	}()

	fn()
}
