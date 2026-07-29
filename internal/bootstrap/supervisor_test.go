package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
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
