//go:build darwin

package vz

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

// vmHostGracePeriod is how long terminateVMHost waits after SIGTERM before
// escalating to SIGKILL.
const vmHostGracePeriod = 10 * time.Second

// sigkillConfirmTimeout bounds how long terminateVMHost waits, after
// sending SIGKILL, for the kernel to actually finish terminating the
// process before giving up and reporting failure. SIGKILL can't be
// blocked or ignored, but delivery still isn't synchronous with the
// kill(2) syscall returning — the process has to be scheduled to receive
// it, which is normally near-instant but not guaranteed to be within the
// same instant, especially on a host already under memory/CPU pressure.
const sigkillConfirmTimeout = 3 * time.Second

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
//
// Returns an error if pid is still alive when this returns (including
// after a SIGKILL escalation) — callers MUST treat that as "still
// running" and refuse to clean up state as if it had stopped. A previous
// version of this function returned unconditionally, believing whatever
// it last attempted had worked; combined with Stop() removing the pidfile
// regardless of the outcome, that let "rask delete" report success while
// the vm-host process (and its VM) kept running — found live during this
// session.
func terminateVMHost(ctx context.Context, pid int, gracePeriod time.Duration) error {
	if !processAlive(pid) {
		return nil
	}

	_ = syscall.Kill(pid, syscall.SIGTERM)

	deadline := time.After(gracePeriod)

	for processAlive(pid) {
		select {
		case <-deadline:
			return killAndConfirm(pid)
		case <-ctx.Done():
			return killAndConfirm(pid)
		case <-time.After(100 * time.Millisecond):
		}
	}

	return nil
}

// killAndConfirm sends SIGKILL to pid and waits up to sigkillConfirmTimeout
// for it to actually die before reporting failure.
func killAndConfirm(pid int) error {
	_ = syscall.Kill(pid, syscall.SIGKILL)

	deadline := time.After(sigkillConfirmTimeout)

	for processAlive(pid) {
		select {
		case <-deadline:
			return fmt.Errorf("vz: process %d did not exit even after SIGKILL", pid)
		case <-time.After(50 * time.Millisecond):
		}
	}

	return nil
}
