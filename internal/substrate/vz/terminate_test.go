//go:build darwin

package vz

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// spawnSleepProcess starts a real, harmless child process (sleep) for
// terminateVMHost's tests to signal — a fake stand-in for a vm-host
// process, without needing any actual VM.
//
// It reaps the child asynchronously in the background for the test's
// entire lifetime: this test process is the child's direct parent, so
// once terminateVMHost signals it, the kernel leaves it as an unreaped
// zombie — which kill(pid, 0) (processAlive's liveness check) still
// reports as "alive" — until something calls Wait() on it. Without this,
// every assertion that a signaled process is no longer alive would
// spuriously fail (found live, matching a documented gotcha from
// internal/substrate/hostproc's own tests: this is a test-harness
// artifact of being the parent, not a real production concern — a real
// vm-host process's parent is a short-lived, already-exited CLI
// invocation, so init reaps it immediately).
func spawnSleepProcess(t *testing.T, seconds int) int {
	t.Helper()

	cmd := exec.Command("/bin/sleep", strconv.Itoa(seconds))

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}

	waitDone := make(chan struct{})

	go func() {
		defer close(waitDone)
		_, _ = cmd.Process.Wait()
	}()

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	return cmd.Process.Pid
}

// requireDeadEventually polls processAlive until it reports false or
// timeout elapses, matching the reap race documented on spawnSleepProcess.
func requireDeadEventually(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Errorf("process %d still alive after %s", pid, timeout)

			return
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminateVMHost_SIGTERMStopsAResponsiveProcess(t *testing.T) {
	t.Parallel()

	pid := spawnSleepProcess(t, 60)

	if err := terminateVMHost(context.Background(), pid, 5*time.Second); err != nil {
		t.Errorf("terminateVMHost: %v", err)
	}

	requireDeadEventually(t, pid, 2*time.Second)
}

func TestTerminateVMHost_NoopWhenAlreadyDead(t *testing.T) {
	t.Parallel()

	pid := spawnSleepProcess(t, 60)

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("pre-killing test process: %v", err)
	}

	requireDeadEventually(t, pid, 2*time.Second)

	// Must not panic or hang when the pid is already gone, and must
	// report success (there's nothing left to terminate).
	if err := terminateVMHost(context.Background(), pid, 5*time.Second); err != nil {
		t.Errorf("terminateVMHost on an already-dead pid: %v", err)
	}
}

func TestTerminateVMHost_EscalatesToSIGKILLOnCtxCancellation(t *testing.T) {
	t.Parallel()

	// Canceling ctx up front exercises the SIGKILL-escalation path
	// deterministically and fast, rather than waiting out the full
	// grace period.
	pid := spawnSleepProcess(t, 60)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := terminateVMHost(ctx, pid, 30*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("terminateVMHost: %v", err)
	}

	requireDeadEventually(t, pid, 2*time.Second)

	if elapsed > 5*time.Second {
		t.Errorf("terminateVMHost took %s with an already-canceled ctx, want fast SIGKILL escalation", elapsed)
	}
}
