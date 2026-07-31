//go:build darwin

package vz

import (
	"context"
	"testing"
	"time"
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
