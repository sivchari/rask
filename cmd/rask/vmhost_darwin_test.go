//go:build darwin

package main

import (
	"syscall"
	"testing"
)

// TestVMHostSignals_IncludesSIGHUP is the regression test for the silent
// vm-host death investigated during this session: newVMHostCommand's RunE
// originally only caught SIGTERM/SIGINT via signal.NotifyContext, so an
// unhandled SIGHUP terminated the process immediately with Go's default
// disposition — no deferred cleanup, no log line. vmHostSignals must
// include SIGHUP so it instead routes through the same graceful shutdown
// path (context cancellation) as SIGTERM/SIGINT.
//
// newVMHostCommand's RunE itself is not exercised here: it requires a real
// --home/--name and calls through to vz.RunVMHost, which boots an actual
// VM — not something a unit test should do. vmHostSignals is extracted
// specifically so this one fact (which signals trigger graceful shutdown)
// is checkable without that.
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
