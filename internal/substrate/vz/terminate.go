//go:build darwin

package vz

import (
	"context"
	"syscall"
	"time"
)

// vmHostGracePeriod is how long terminateVMHost waits after SIGTERM before
// escalating to SIGKILL.
const vmHostGracePeriod = 10 * time.Second

// terminateVMHost sends SIGTERM to pid — RunVMHost's own signal handling
// (cmd/rask/vmhost_darwin.go) turns this into a clean VM shutdown via
// context cancellation and its own deferred stopVM/console.Close/net.Close
// calls — waiting up to gracePeriod (or until ctx is done, whichever comes
// first) before escalating to SIGKILL. A no-op if pid is already dead.
//
// SIGKILL must never be the *first* resort here: it can't be caught, so
// RunVMHost's cleanup defers (which stop the actual
// Virtualization.framework VM) would never run, orphaning a live VM
// process that keeps consuming real host memory/CPU with nothing left to
// stop it — confirmed as a real, reproducible risk during this session's
// own incident investigation (a VM process was found still running,
// unowned, after its controlling process had died).
func terminateVMHost(ctx context.Context, pid int, gracePeriod time.Duration) {
	if !processAlive(pid) {
		return
	}

	_ = syscall.Kill(pid, syscall.SIGTERM)

	deadline := time.After(gracePeriod)

	for processAlive(pid) {
		select {
		case <-deadline:
			_ = syscall.Kill(pid, syscall.SIGKILL)

			return
		case <-ctx.Done():
			_ = syscall.Kill(pid, syscall.SIGKILL)

			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
