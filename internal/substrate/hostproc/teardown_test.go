//go:build linux

package hostproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRuntime_StopWithNoStateFileIsANoOp(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	if err := os.MkdirAll(r.dataDir("dev"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := r.Stop(context.Background(), "dev"); err != nil {
		t.Errorf("Stop() with no state file = %v, want nil", err)
	}
}

func TestRuntime_StopKillsPersistedPIDsAndRemovesRunningMarker(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())
	name := "dev"

	if err := os.MkdirAll(r.dataDir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleeper: %v", err)
	}

	pid := cmd.Process.Pid

	// In production, Stop's target processes are reparented to init (PID
	// 1) by the time it runs — the "rask create" process that spawned
	// them has long since exited — so init reaps them the instant they
	// die and kill(pid, 0) correctly reports ESRCH. In this test, this
	// test binary is still their direct parent, so without an active
	// Wait a killed child lingers as a zombie: still a valid PID that
	// kill(pid, 0) reports as "alive" even though it's dead. Reap
	// asynchronously, like init would, so the liveness check below is
	// meaningful.
	waited := make(chan struct{})

	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	state := runtimeState{DatastorePID: pid, ProcessPIDs: map[string]int{}}
	if err := writeState(r.statePath(name), state); err != nil {
		t.Fatalf("writeState: %v", err)
	}

	if err := os.WriteFile(r.runningMarkerPath(name), nil, 0o644); err != nil {
		t.Fatalf("writing running marker: %v", err)
	}

	if err := r.Stop(context.Background(), name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("process was not reaped within 1s of Stop returning")
	}

	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		t.Errorf("pid %d is still alive after Stop", pid)
	}

	if _, err := os.Stat(r.runningMarkerPath(name)); !os.IsNotExist(err) {
		t.Errorf("running marker still present after Stop: err=%v", err)
	}
}

func TestRuntime_DeleteWhileRunningErrors(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())
	name := "dev"

	if err := os.MkdirAll(r.dataDir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := os.WriteFile(r.runningMarkerPath(name), nil, 0o644); err != nil {
		t.Fatalf("writing running marker: %v", err)
	}

	if err := r.Delete(context.Background(), name); err == nil {
		t.Fatal("Delete() while running = nil error, want error")
	}

	if _, err := os.Stat(r.dataDir(name)); err != nil {
		t.Errorf("data dir was removed despite Delete failing: %v", err)
	}
}

func TestRuntime_DeleteAfterStopRemovesDataDir(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())
	name := "dev"

	if err := os.MkdirAll(r.dataDir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := r.Delete(context.Background(), name); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(r.dataDir(name)); !os.IsNotExist(err) {
		t.Errorf("data dir still present after Delete: err=%v", err)
	}
}

func TestUnmountUnder_NoMountsIsANoOp(t *testing.T) {
	t.Parallel()

	// Just verifying it doesn't panic or hang on an ordinary directory
	// with nothing mounted under it.
	unmountUnder(filepath.Join(t.TempDir(), "nothing-mounted-here"))
}

func TestKillAll_SkipsAlreadyDeadPIDs(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running short-lived process: %v", err)
	}

	// The PID is now free to be reused by the OS, or simply refers to a
	// reaped process; killAll must not panic either way. Give the kernel
	// a moment to finish reaping.
	time.Sleep(10 * time.Millisecond)

	killAll([]int{cmd.Process.Pid}, syscall.SIGTERM)
}

func TestLookupPIDs_ReturnsOnlyRequestedNamesThatArePresent(t *testing.T) {
	t.Parallel()

	pids := map[string]int{"a": 1, "b": 2, "c": 3}

	got := lookupPIDs(pids, "a", "c", "missing")

	want := []int{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("lookupPIDs() = %v, want %v", got, want)
	}
}

// startProcessWithCwd starts a real, short-lived-but-outlives-the-test
// process (a shell sleeping) with its working directory set to cwd,
// standing in for a containerd-shim-runc-v2 process's own bundle-directory
// cwd (see findProcessByCwd's doc comment). The process is killed via
// t.Cleanup so a failed assertion never leaks it.
func startProcessWithCwd(t *testing.T, cwd string) int {
	t.Helper()

	cmd := exec.Command("/bin/sleep", "30")
	cmd.Dir = cwd

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting process with cwd %s: %v", cwd, err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// See TestRuntime_StopKillsPersistedPIDsAndRemovesRunningMarker's
	// comment: this test binary is the process's direct parent, so it
	// must be reaped for later liveness checks (syscall.Kill(pid, 0)) to
	// be meaningful instead of reporting a zombie as "alive".
	go func() { _ = cmd.Wait() }()

	return cmd.Process.Pid
}

func TestFindProcessByCwd_FindsExactMatchOnly(t *testing.T) {
	t.Parallel()

	bundleDir := t.TempDir()
	otherDir := t.TempDir()

	bundlePID := startProcessWithCwd(t, bundleDir)
	startProcessWithCwd(t, otherDir)

	got, ok := findProcessByCwd(bundleDir)
	if !ok {
		t.Fatal("findProcessByCwd() = not found, want a match")
	}

	if got != bundlePID {
		t.Errorf("findProcessByCwd() = %d, want %d", got, bundlePID)
	}
}

func TestFindProcessByCwd_NoMatchReturnsFalse(t *testing.T) {
	t.Parallel()

	_, ok := findProcessByCwd(filepath.Join(t.TempDir(), "nobody-runs-here"))
	if ok {
		t.Error("findProcessByCwd() = found a match, want none")
	}
}

func TestKillOrphanedShims_KillsExactBundleProcessAndLeavesOthersAlone(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	bundleDir := filepath.Join(dataDir, "containerd", "state", "io.containerd.runtime.v2.task", "k8s.io", "task1")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	shimPID := startProcessWithCwd(t, bundleDir)

	unrelatedDir := t.TempDir()
	unrelatedPID := startProcessWithCwd(t, unrelatedDir)

	killOrphanedShims(dataDir)

	// Give the kernel a moment to deliver SIGKILL.
	time.Sleep(50 * time.Millisecond)

	if err := syscall.Kill(shimPID, syscall.Signal(0)); err == nil {
		t.Errorf("shim pid %d is still alive after killOrphanedShims", shimPID)
	}

	if err := syscall.Kill(unrelatedPID, syscall.Signal(0)); err != nil {
		t.Errorf("unrelated pid %d was killed by killOrphanedShims, want it left alone: %v", unrelatedPID, err)
	}
}

func TestKillOrphanedShims_NoBundlesIsANoOp(t *testing.T) {
	t.Parallel()

	// Just verifying it doesn't panic or error on a cluster that never
	// ran a pod (no io.containerd.runtime.v2.task dir at all).
	killOrphanedShims(t.TempDir())
}
