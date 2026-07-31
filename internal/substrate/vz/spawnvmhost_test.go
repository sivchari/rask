//go:build darwin

package vz

import (
	"os/exec"
	"testing"
)

// TestProcessRelease_MustCapturePidBeforeCalling documents and guards the
// exact stdlib gotcha that produced a real, session-long bug in
// spawnVMHost: os.Process.Release's own doc comment says "for historical
// reasons, on systems other than Windows, Release sets the Pid field to
// -1". spawnVMHost originally read cmd.Process.Pid *after* calling
// Release(), so it always wrote -1 into every cluster's pidfile — readPID's
// pid<=0 guard silently treated that as "not running" instead of erroring,
// so Stop/Delete believed there was nothing to terminate and no-op'd
// successfully while the real vm-host process (and its VM) kept running,
// unowned. Found live during this session, after multiple "successful"
// rask delete runs left orphaned VMs behind.
//
// This test spawns a real, harmless child process (mirroring spawnVMHost's
// own Start-then-Release sequence) to prove the ordering requirement
// against the actual stdlib behavior on this platform, rather than
// exercising spawnVMHost itself (which shells out to os.Executable(), not
// a substitutable target for a unit test).
func TestProcessRelease_MustCapturePidBeforeCalling(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}

	pidBeforeRelease := cmd.Process.Pid
	if pidBeforeRelease <= 0 {
		t.Fatalf("cmd.Process.Pid before Release() = %d, want a positive pid", pidBeforeRelease)
	}

	if err := cmd.Process.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if cmd.Process.Pid != -1 {
		t.Fatalf("cmd.Process.Pid after Release() = %d, want -1 (stdlib behavior spawnVMHost must work around by capturing the pid first)", cmd.Process.Pid)
	}

	if pidBeforeRelease == -1 {
		t.Fatal("pid captured before Release() was already -1, which would defeat this test's own premise")
	}
}
