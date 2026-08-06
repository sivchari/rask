//go:build darwin

package vz

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
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

// TestSpawnVMHost_UnreapedExitedChildStillAnswersProcessAlive is the
// regression test for a bug found live testing pkg/cluster as a library
// end to end: terminateVMHost's SIGKILL was delivered and the vm-host
// process genuinely exited, yet Stop still reported "did not exit even
// after SIGKILL". Root cause was spawnVMHost's original
// cmd.Process.Release() call, which stops this package's own bookkeeping
// of the child but never wait(2)s it — POSIX leaves an exited-but-unreaped
// child as a zombie, and kill(pid, 0) (processAlive's own liveness check)
// still succeeds against a zombie's pid, since the pid is not freed until
// something reaps it. This was invisible for the rask CLI's own
// short-lived "rask create" (its own process exit reparents any zombie
// child to launchd, which promptly reaps it), but not for a
// pkg/cluster.Provider caller that stays alive across both Create and a
// later Stop/Delete of the same cluster within one long-running process
// (rask's actual target library use case, e.g. fjord) — nothing reparents
// the zombie away in that shape.
//
// This proves the underlying OS behavior spawnVMHost's fix (an async
// cmd.Process.Wait() instead of Release() — see its doc comment) depends
// on, against a real child process rather than spawnVMHost itself (which
// shells out to os.Executable() and is not a substitutable target for a
// unit test — see this file's other tests for the same constraint):
// processAlive reports an exited-but-unreaped child as alive until
// something actually reaps it.
func TestSpawnVMHost_UnreapedExitedChildStillAnswersProcessAlive(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting child: %v", err)
	}

	pid := cmd.Process.Pid

	// Give the child (a no-op binary) ample time to actually exit, without
	// this test process ever calling Wait — mirroring the pre-fix
	// spawnVMHost, which released the child instead.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !processAlive(pid) {
		t.Fatal("processAlive(pid) = false for an exited-but-unreaped (zombie) child, want true — this test's premise (that a zombie still answers kill(pid, 0)) does not hold on this platform")
	}

	// The fix: reap it. processAlive must now correctly report it gone.
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if processAlive(pid) {
		t.Error("processAlive(pid) = true after reaping the child with Wait, want false")
	}
}
