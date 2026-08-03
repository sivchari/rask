//go:build darwin

package vz

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	cvz "github.com/Code-Hex/vz/v3"
)

// runRecoverableTimeout bounds how long these tests wait for cancel to
// fire, so a regression that drops the recover (and lets the panic crash
// the test binary, or silently swallows it without calling cancel) fails
// the test instead of hanging.
const runRecoverableTimeout = time.Second

// TestRunRecoverable_RecoversPanicAndCancels is the regression test for
// the leaked-VM-XPC-process investigation: an unrecovered panic in
// RunVMHost's background goroutines (the console logger, the boot
// watchdog) would crash the whole vm-host process without ever running
// its deferred stopVM/console.Close/net.Close/lock.Release calls, since
// those defers live in a different (the main) goroutine — see
// runRecoverable's own doc comment. Recovering and calling cancel instead
// must route the failure back through the normal runCtx.Done() path.
func TestRunRecoverable_RecoversPanicAndCancels(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})

	go func() {
		defer close(done)
		runRecoverable("test", cancel, func() {
			panic("boom")
		})
	}()

	select {
	case <-done:
	case <-time.After(runRecoverableTimeout):
		t.Fatal("runRecoverable did not return after fn panicked, want it to recover and return")
	}

	select {
	case <-ctx.Done():
	case <-time.After(runRecoverableTimeout):
		t.Fatal("runRecoverable recovered a panic but never called cancel")
	}
}

// TestRunRecoverable_NoPanicDoesNotCancel guards against an
// overcorrection: runRecoverable must not call cancel on an ordinary,
// panic-free return (e.g. logConsoleLines' console channel simply
// closing on a clean VM shutdown) — only an actual panic should route
// through the failure path.
func TestRunRecoverable_NoPanicDoesNotCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := false
	runRecoverable("test", cancel, func() { ran = true })

	if !ran {
		t.Fatal("runRecoverable did not run fn")
	}

	select {
	case <-ctx.Done():
		t.Fatal("runRecoverable called cancel even though fn did not panic")
	default:
	}
}

// TestHandleVMStateChange is the regression test for the silent vm-host
// death investigated during this session: a vm-host process could
// disappear with zero trace in vm-host.log because nothing logged what vz
// reported (or failed to report) about the VM's state. handleVMStateChange
// must log every transition it is given — terminal or not — and must only
// return a non-nil error for the two terminal states (Stopped/Error).
func TestHandleVMStateChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   cvz.VirtualMachineState
		wantErr bool
	}{
		{"running is not terminal", cvz.VirtualMachineStateRunning, false},
		{"paused is not terminal", cvz.VirtualMachineStatePaused, false},
		{"starting is not terminal", cvz.VirtualMachineStateStarting, false},
		{"stopped is terminal", cvz.VirtualMachineStateStopped, true},
		{"error is terminal", cvz.VirtualMachineStateError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := handleVMStateChange(&buf, tt.state)
			if (err != nil) != tt.wantErr {
				t.Errorf("handleVMStateChange(%v) error = %v, wantErr %v", tt.state, err, tt.wantErr)
			}

			if !strings.Contains(buf.String(), "vz: virtual machine state changed to") {
				t.Errorf("handleVMStateChange(%v) did not log the transition to w, got %q", tt.state, buf.String())
			}
		})
	}
}
