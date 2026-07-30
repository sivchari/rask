//go:build darwin

package vz_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/substrate/vz"
)

func TestNew_ReturnsDistinctInstances(t *testing.T) {
	t.Parallel()

	a := vz.New(t.TempDir())
	b := vz.New(t.TempDir())

	if a == b {
		t.Error("New() returned the same instance twice, want independent instances")
	}
}

func TestRuntime_Stop_NoopWhenNotRunning(t *testing.T) {
	t.Parallel()

	r := vz.New(t.TempDir())

	if err := r.Stop(context.Background(), "never-started"); err != nil {
		t.Errorf("Stop on a never-started cluster = %v, want nil (idempotent no-op)", err)
	}
}

func TestRuntime_Delete_SucceedsWhenNotRunning(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir)

	// Simulate a cluster directory left behind without a running
	// vm-host (e.g. after Stop already ran).
	clusterDir := filepath.Join(homeDir, "clusters", "dev")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	if err := r.Delete(context.Background(), "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(clusterDir); !os.IsNotExist(err) {
		t.Errorf("cluster dir still exists after Delete: err=%v", err)
	}
}

func TestRuntime_Delete_ErrorsWhenPIDFilePresent(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir)

	clusterDir := filepath.Join(homeDir, "clusters", "dev")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	// A real, currently-running PID (this test process's own) so
	// Delete's "still running" check has something plausible to find,
	// without needing a real vm-host process.
	if err := os.WriteFile(filepath.Join(clusterDir, "vm-host.pid"), []byte("1"), 0o600); err != nil {
		t.Fatalf("seeding pidfile: %v", err)
	}

	if err := r.Delete(context.Background(), "dev"); err == nil {
		t.Fatal("Delete with a pidfile present = nil error, want error (cluster still running)")
	}
}

// TestRuntime_Delete_TreatsMalformedPIDAsNotRunning guards against a real
// incident risk found live: a leftover pidfile containing "-1" (written
// externally, not by Runtime.Start, which always writes a real
// cmd.Process.Pid) must never reach syscall.Kill. POSIX kill(2) treats pid
// -1 as "every process the caller may signal" and pid 0 as "every process
// in the caller's process group" — feeding either into Stop's
// SIGTERM/SIGKILL calls would broadcast a signal far outside this one
// cluster's vm-host process.
func TestRuntime_Delete_TreatsMalformedPIDAsNotRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		pidContents string
	}{
		{name: "negative one", pidContents: "-1"},
		{name: "zero", pidContents: "0"},
		{name: "negative", pidContents: "-42"},
		{name: "empty", pidContents: ""},
		{name: "non-numeric", pidContents: "not-a-pid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			r := vz.New(homeDir)

			clusterDir := filepath.Join(homeDir, "clusters", "dev")
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				t.Fatalf("seeding cluster dir: %v", err)
			}

			if err := os.WriteFile(filepath.Join(clusterDir, "vm-host.pid"), []byte(tt.pidContents), 0o600); err != nil {
				t.Fatalf("seeding pidfile: %v", err)
			}

			// Stop must treat this as "not running" (a no-op), not
			// attempt to signal the malformed pid.
			if err := r.Stop(context.Background(), "dev"); err != nil {
				t.Errorf("Stop with pidfile content %q = %v, want nil", tt.pidContents, err)
			}

			// Delete must likewise proceed (not report "still
			// running") and actually remove the directory.
			if err := r.Delete(context.Background(), "dev"); err != nil {
				t.Fatalf("Delete with pidfile content %q = %v, want nil", tt.pidContents, err)
			}

			if _, err := os.Stat(clusterDir); !os.IsNotExist(err) {
				t.Errorf("cluster dir still exists after Delete: err=%v", err)
			}
		})
	}
}

func TestRuntime_ExecWriteFilePortForward_ErrorWhenNotRunning(t *testing.T) {
	t.Parallel()

	r := vz.New(t.TempDir())
	ctx := context.Background()

	if _, err := r.Exec(ctx, "never-started", io.Discard, "true"); err == nil {
		t.Error("Exec on a never-started cluster = nil error, want error")
	}

	if err := r.WriteFile(ctx, "never-started", "/etc/x", nil); err == nil {
		t.Error("WriteFile on a never-started cluster = nil error, want error")
	}

	if _, err := r.PortForward(ctx, "never-started", "127.0.0.1:0", "127.0.0.1:0"); err == nil {
		t.Error("PortForward on a never-started cluster = nil error, want error")
	}
}
