//go:build darwin

package vz

import (
	"os"
	"os/exec"
	"syscall"
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

// TestSpawnVMHost_DetachesIntoNewSession is the regression test for the
// silent vm-host death investigated during this session: a healthy vm-host
// process died ~100s into a cluster's life with zero trace in vm-host.log,
// correlated with an unrelated sibling background process being killed by
// an external harness. spawnVMHost originally used only Setpgid, which
// detaches the new process's *group* but leaves it in its parent's
// *session* — reachable by that session's own signal delivery (e.g. a
// controlling terminal hanging up) for the rest of its life, not just at
// spawn time. Setsid must be used instead, which makes the child both
// session leader and process group leader of a brand new session.
//
// This spawns a real child process (mirroring spawnVMHost's own SysProcAttr,
// without exercising spawnVMHost itself, which shells out to
// os.Executable() and is not a substitutable target for a unit test — see
// TestProcessRelease_MustCapturePidBeforeCalling's doc comment for the same
// constraint) and asserts its session id differs from this test process's
// own, and equals its own pid (the definition of a session leader).
func TestSpawnVMHost_DetachesIntoNewSession(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/sleep", "5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}

	childPID := cmd.Process.Pid

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	ownSID, err := syscall.Getsid(os.Getpid())
	if err != nil {
		t.Fatalf("Getsid(self): %v", err)
	}

	childSID, err := syscall.Getsid(childPID)
	if err != nil {
		t.Fatalf("Getsid(child): %v", err)
	}

	if childSID == ownSID {
		t.Fatalf("child session id = %d, same as this test process's session id %d; Setsid should detach the child into its own new session so it is no longer reachable by this session's signal delivery (Setpgid alone leaves the child in this same session — see spawnVMHost's doc comment)", childSID, ownSID)
	}

	if childSID != childPID {
		t.Fatalf("child session id = %d, want %d (its own pid): Setsid should make the child the leader of a brand new session", childSID, childPID)
	}
}
