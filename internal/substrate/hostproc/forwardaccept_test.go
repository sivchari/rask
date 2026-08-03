//go:build linux

package hostproc

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

const testPodCIDR = "10.244.0.0/16"

// fakeIPTables records every call made through its run method (an
// iptablesRunner) and returns the configured result for each verb,
// letting tests control ensureForwardAcceptPodCIDRWith's and
// removeForwardAcceptPodCIDRWith's view of iptables state without a real
// iptables binary.
type fakeIPTables struct {
	calls [][]string

	// checkErr is returned for every "-C" call; nil means the rule
	// already exists, a non-nil error means it doesn't (or a genuine
	// failure, depending on what kind of error it is).
	checkErr error
	// insertErr is returned for every "-I" call.
	insertErr error
	// deleteErr is returned for every "-D" call.
	deleteErr error
}

func (f *fakeIPTables) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))

	switch args[0] {
	case "-C":
		return nil, f.checkErr
	case "-I":
		return nil, f.insertErr
	case "-D":
		return nil, f.deleteErr
	default:
		return nil, nil
	}
}

// exitError returns a real *exec.ExitError with the given exit code,
// standing in for what "iptables -C" returns when the rule it's checking
// for doesn't exist (documented to exit 1 in that case).
func exitError(t *testing.T, code int) error {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected command to exit non-zero")
	}

	return err
}

func TestEnsureForwardAcceptPodCIDRWith_InsertsBothDirectionsWhenAbsent(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{checkErr: exitError(t, 1)}

	if err := ensureForwardAcceptPodCIDRWith(testPodCIDR, f.run); err != nil {
		t.Fatalf("ensureForwardAcceptPodCIDRWith: %v", err)
	}

	want := [][]string{
		{"-C", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
		{"-I", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
		{"-C", "FORWARD", "-d", testPodCIDR, "-j", "ACCEPT"},
		{"-I", "FORWARD", "-d", testPodCIDR, "-j", "ACCEPT"},
	}

	assertCalls(t, f.calls, want)
}

func TestEnsureForwardAcceptPodCIDRWith_SkipsInsertWhenAlreadyPresent(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{checkErr: nil}

	if err := ensureForwardAcceptPodCIDRWith(testPodCIDR, f.run); err != nil {
		t.Fatalf("ensureForwardAcceptPodCIDRWith: %v", err)
	}

	want := [][]string{
		{"-C", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
		{"-C", "FORWARD", "-d", testPodCIDR, "-j", "ACCEPT"},
	}

	assertCalls(t, f.calls, want)
}

func TestEnsureForwardAcceptPodCIDRWith_CheckGenuineFailureReturnsActionableError(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{checkErr: errors.New("iptables: command not found")}

	err := ensureForwardAcceptPodCIDRWith(testPodCIDR, f.run)
	if err == nil {
		t.Fatal("ensureForwardAcceptPodCIDRWith() = nil error, want error")
	}

	if !strings.Contains(err.Error(), "ensure iptables is installed") {
		t.Errorf("error = %q, want it to contain actionable guidance", err.Error())
	}

	if !strings.Contains(err.Error(), "command not found") {
		t.Errorf("error = %q, want it to wrap the underlying failure", err.Error())
	}
}

func TestEnsureForwardAcceptPodCIDRWith_InsertFailureReturnsActionableError(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{
		checkErr:  exitError(t, 1),
		insertErr: errors.New("permission denied"),
	}

	err := ensureForwardAcceptPodCIDRWith(testPodCIDR, f.run)
	if err == nil {
		t.Fatal("ensureForwardAcceptPodCIDRWith() = nil error, want error")
	}

	if !strings.Contains(err.Error(), "ensure iptables is installed") {
		t.Errorf("error = %q, want it to contain actionable guidance", err.Error())
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want it to wrap the underlying failure", err.Error())
	}

	// Fails on the first direction ("-s"), so the second direction's
	// rule must never be attempted.
	want := [][]string{
		{"-C", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
		{"-I", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
	}

	assertCalls(t, f.calls, want)
}

func TestRemoveForwardAcceptPodCIDRWith_RemovesBothDirections(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{}

	removeForwardAcceptPodCIDRWith(testPodCIDR, f.run)

	want := [][]string{
		{"-D", "FORWARD", "-s", testPodCIDR, "-j", "ACCEPT"},
		{"-D", "FORWARD", "-d", testPodCIDR, "-j", "ACCEPT"},
	}

	assertCalls(t, f.calls, want)
}

func TestRemoveForwardAcceptPodCIDRWith_IgnoresDeleteFailures(t *testing.T) {
	t.Parallel()

	f := &fakeIPTables{deleteErr: errors.New("no such rule")}

	// Must not panic and must still attempt both directions even though
	// every delete "fails" — this is best-effort teardown, matching
	// removeCNIBridge.
	removeForwardAcceptPodCIDRWith(testPodCIDR, f.run)

	if len(f.calls) != 2 {
		t.Errorf("len(calls) = %d, want 2", len(f.calls))
	}
}

func assertCalls(t *testing.T, got, want [][]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	for i := range want {
		if !equalArgs(got[i], want[i]) {
			t.Errorf("call[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
