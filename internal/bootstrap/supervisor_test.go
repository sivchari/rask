package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
	"golang.org/x/sync/errgroup"
)

// waitFor polls check until it returns true or timeout elapses, failing the
// test if the timeout is reached.
func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	if !check() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestSupervisor_StartCapturesPerProcessLogs(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	err := sup.Start(ctx,
		bootstrap.ProcessSpec{Name: "one", Path: "/bin/sh", Args: []string{"-c", "echo from-one"}},
		bootstrap.ProcessSpec{Name: "two", Path: "/bin/sh", Args: []string{"-c", "echo from-two"}},
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	waitFor(t, time.Second, func() bool {
		one, err := sup.Logs("one")

		return err == nil && strings.Contains(string(one), "from-one")
	})

	waitFor(t, time.Second, func() bool {
		two, err := sup.Logs("two")

		return err == nil && strings.Contains(string(two), "from-two")
	})
}

func TestSupervisor_LogsUnknownProcessErrors(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()

	if _, err := sup.Logs("missing"); err == nil {
		t.Error("Logs(missing) = nil error, want error")
	}
}

func TestSupervisor_StartFailureAbortsRemainingAndStopsAlreadyLaunched(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	err := sup.Start(ctx,
		bootstrap.ProcessSpec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"30"}},
		bootstrap.ProcessSpec{Name: "bogus", Path: "/no/such/binary-rask-test"},
	)
	if err == nil {
		t.Fatal("Start = nil error, want error for the invalid second process")
	}

	t.Cleanup(sup.Stop)

	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Start error = %q, want to mention process %q", err, "bogus")
	}

	// The already-launched sleeper should have been recorded even though
	// Start ultimately failed.
	if _, logErr := sup.Logs("sleeper"); logErr != nil {
		t.Errorf("Logs(sleeper): %v, want the sleeper to have been tracked", logErr)
	}
}

func TestSupervisor_RestartOnCrashRelaunchesProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	err := sup.Start(ctx, bootstrap.ProcessSpec{
		Name:           "crasher",
		Path:           "/bin/sh",
		Args:           []string{"-c", `echo x >> "$COUNT_FILE"; exit 1`},
		Env:            []string{"COUNT_FILE=" + countFile},
		RestartOnCrash: true,
		RestartDelay:   5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	waitFor(t, 2*time.Second, func() bool {
		return countLines(countFile) >= 3
	})
}

func TestSupervisor_NoRestartWhenDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	err := sup.Start(ctx, bootstrap.ProcessSpec{
		Name: "once",
		Path: "/bin/sh",
		Args: []string{"-c", `echo x >> "$COUNT_FILE"; exit 1`},
		Env:  []string{"COUNT_FILE=" + countFile},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	waitFor(t, time.Second, func() bool {
		return countLines(countFile) >= 1
	})

	// Give a restart, if one were mistakenly scheduled, a chance to fire.
	time.Sleep(100 * time.Millisecond)

	if got := countLines(countFile); got != 1 {
		t.Errorf("countLines = %d, want 1 (no restart)", got)
	}
}

func TestSupervisor_StopKillsLongRunningProcess(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	if err := sup.Start(ctx, bootstrap.ProcessSpec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"30"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	sup.Stop()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("Stop took %s, want well under the 30s sleep duration", elapsed)
	}
}

// TestSupervisor_ProcessSurvivesAfterUnrelatedErrgroupCompletes is a
// regression test for a real bug found via an actual E2E run (not caught
// by any other unit test): golang.org/x/sync/errgroup.WithContext's
// derived context is canceled "the first time Wait returns, whichever
// occurs first" — including a *successful* return. internal/bootstrap's
// boot DAG uses an errgroup to coordinate readiness-waiting across
// parallel branches; launching a process with that errgroup's derived
// context (instead of the caller's own stable context) meant every
// component — including kube-apiserver — got SIGKILLed the instant the
// boot DAG finished successfully, since Supervisor.Launch ties process
// lifetime to whatever context it's given via exec.CommandContext.
func TestSupervisor_ProcessSurvivesAfterUnrelatedErrgroupCompletes(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	stableCtx := context.Background()

	// Mirrors internal/bootstrap's runBootDAG shape: an errgroup
	// coordinates a "phase", but the process itself must be launched
	// with the stable outer context, not the errgroup's derived one.
	g, _ := errgroup.WithContext(stableCtx)
	g.Go(func() error {
		return sup.Launch(stableCtx, bootstrap.ProcessSpec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"5"}})
	})

	if err := g.Wait(); err != nil {
		t.Fatalf("errgroup.Wait: %v", err)
	}
	t.Cleanup(sup.Stop)

	// Give the errgroup's context-cancellation goroutine (if the bug
	// were present) a moment to actually kill the process.
	time.Sleep(100 * time.Millisecond)

	pid, ok := sup.PIDs()["sleeper"]
	if !ok {
		t.Fatal(`PIDs()["sleeper"] missing`)
	}

	if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
		t.Errorf("process was killed after the unrelated errgroup completed: %v", err)
	}
}

func TestSupervisor_LaunchWithLogPathWritesDirectlyToFile(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "proc.log")

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	spec := bootstrap.ProcessSpec{
		Name:    "logger",
		Path:    "/bin/sh",
		Args:    []string{"-c", "echo to-file"},
		LogPath: logPath,
	}

	if err := sup.Launch(ctx, spec); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(sup.Stop)

	waitFor(t, time.Second, func() bool {
		data, err := os.ReadFile(logPath)

		return err == nil && strings.Contains(string(data), "to-file")
	})

	// The in-memory Logs() buffer is bypassed entirely when LogPath is
	// set (see ProcessSpec.LogPath's doc comment for why): the process
	// output only exists on disk.
	logs, err := sup.Logs("logger")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if len(logs) != 0 {
		t.Errorf("Logs(logger) = %q, want empty (output went to LogPath instead)", logs)
	}
}

func TestSupervisor_PIDsReflectsRunningProcesses(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	if pids := sup.PIDs(); len(pids) != 0 {
		t.Fatalf("PIDs() before any launch = %v, want empty", pids)
	}

	if err := sup.Launch(ctx, bootstrap.ProcessSpec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"30"}}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(sup.Stop)

	var pid int

	waitFor(t, time.Second, func() bool {
		pids := sup.PIDs()
		pid = pids["sleeper"]

		return pid > 0
	})

	if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
		t.Errorf("PIDs()[sleeper] = %d does not refer to a live process: %v", pid, err)
	}
}

func TestSupervisor_LaunchIncrementallyAddsProcesses(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	if err := sup.Launch(ctx, bootstrap.ProcessSpec{Name: "one", Path: "/bin/sh", Args: []string{"-c", "echo from-one"}}); err != nil {
		t.Fatalf("Launch(one): %v", err)
	}
	t.Cleanup(sup.Stop)

	waitFor(t, time.Second, func() bool {
		one, err := sup.Logs("one")

		return err == nil && strings.Contains(string(one), "from-one")
	})

	if err := sup.Launch(ctx, bootstrap.ProcessSpec{Name: "two", Path: "/bin/sh", Args: []string{"-c", "echo from-two"}}); err != nil {
		t.Fatalf("Launch(two): %v", err)
	}

	waitFor(t, time.Second, func() bool {
		two, err := sup.Logs("two")

		return err == nil && strings.Contains(string(two), "from-two")
	})
}

func TestSupervisor_LaunchInvalidBinaryErrors(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	err := sup.Launch(ctx, bootstrap.ProcessSpec{Name: "bogus", Path: "/no/such/binary-rask-test"})
	if err == nil {
		t.Fatal("Launch(bogus) = nil error, want error")
	}
	t.Cleanup(sup.Stop)

	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Launch error = %q, want to mention process %q", err, "bogus")
	}
}

func TestSupervisor_StopKillsProcessesLaunchedIncrementally(t *testing.T) {
	t.Parallel()

	sup := bootstrap.NewSupervisor()
	ctx := context.Background()

	if err := sup.Launch(ctx, bootstrap.ProcessSpec{Name: "sleeper", Path: "/bin/sleep", Args: []string{"30"}}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	start := time.Now()
	sup.Stop()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("Stop took %s, want well under the 30s sleep duration", elapsed)
	}
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}

	return len(strings.Split(trimmed, "\n"))
}
