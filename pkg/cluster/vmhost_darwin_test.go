//go:build darwin

package cluster

import (
	"os"
	"syscall"
	"testing"
)

// TestVMHostSignals_IncludesSIGHUP is the regression test for the silent
// vm-host death investigated when this signal set was first introduced
// (originally in cmd/rask, moved here with RunVMHostIfRequested itself so
// there is exactly one implementation of vm-host's signal handling): an
// unhandled SIGHUP terminates a Go process immediately with no deferred
// cleanup and no log line unless something explicitly catches it.
// vmHostSignals must include SIGHUP so it instead routes through the same
// graceful shutdown path (context cancellation) as SIGTERM/SIGINT.
//
// RunVMHostIfRequested's actual vz.RunVMHost call is not exercised here: it
// requires a real --home/--name and boots an actual VM — not something a
// unit test should do. vmHostSignals is extracted specifically so this one
// fact (which signals trigger graceful shutdown) is checkable without that.
func TestVMHostSignals_IncludesSIGHUP(t *testing.T) {
	t.Parallel()

	want := map[syscall.Signal]bool{
		syscall.SIGTERM: false,
		syscall.SIGINT:  false,
		syscall.SIGHUP:  false,
	}

	for _, sig := range vmHostSignals {
		s, ok := sig.(syscall.Signal)
		if !ok {
			t.Fatalf("vmHostSignals contains non-syscall.Signal value %v (%T)", sig, sig)
		}

		if _, tracked := want[s]; !tracked {
			continue
		}

		want[s] = true
	}

	for sig, found := range want {
		if !found {
			t.Errorf("vmHostSignals does not include %v, want it present so vm-host shuts down gracefully instead of dying silently", sig)
		}
	}

	if len(vmHostSignals) != len(want) {
		t.Errorf("vmHostSignals has %d entries, want exactly %d (%v): an extra signal here is easy to add by accident and changes vm-host's shutdown behavior", len(vmHostSignals), len(want), vmHostSignals)
	}
}

// setArgs temporarily replaces os.Args with args, restoring the original at
// test cleanup. Not run in parallel with other tests in this package that
// also mutate os.Args.
func setArgs(t *testing.T, args []string) {
	t.Helper()

	orig := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = orig })
}

// TestRunVMHostIfRequested_NotRequestedReturnsImmediately proves an
// ordinary invocation (the overwhelmingly common case) returns
// handled=false without attempting to boot anything, so a library consumer
// can call this unconditionally as the first line of main.
func TestRunVMHostIfRequested_NotRequestedReturnsImmediately(t *testing.T) {
	setArgs(t, []string{"a-consumer-binary", "some-other-flag"})

	handled, err := RunVMHostIfRequested()
	if handled {
		t.Error("RunVMHostIfRequested() handled = true for an ordinary invocation, want false")
	}

	if err != nil {
		t.Errorf("RunVMHostIfRequested() err = %v, want nil", err)
	}
}

// TestRunVMHostIfRequested_MissingFlagsReturnsHandledWithError proves a
// malformed vm-host invocation is reported as handled=true with a
// descriptive error (never silently ignored, and never falls through to
// vz.RunVMHost with empty home/name).
func TestRunVMHostIfRequested_MissingFlagsReturnsHandledWithError(t *testing.T) {
	setArgs(t, []string{"a-consumer-binary", vmHostArg})

	handled, err := RunVMHostIfRequested()
	if !handled {
		t.Fatal("RunVMHostIfRequested() handled = false for a __vm-host invocation, want true")
	}

	if err == nil {
		t.Error("RunVMHostIfRequested() with no --home/--name = nil error, want error")
	}
}
