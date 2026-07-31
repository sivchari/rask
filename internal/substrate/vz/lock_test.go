//go:build darwin

package vz

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireVMLock_SecondAcquireFails(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	lock1, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("first acquireVMLock: %v", err)
	}
	defer lock1.Release()

	_, err = acquireVMLock(homeDir, "t1")
	if !errors.Is(err, ErrAnotherVMRunning) {
		t.Fatalf("second acquireVMLock error = %v, want ErrAnotherVMRunning", err)
	}

	if !strings.Contains(err.Error(), `"dev"`) {
		t.Errorf("second acquireVMLock error = %q, want it to name the holder cluster %q", err.Error(), "dev")
	}
}

func TestAcquireVMLock_ReleasedLockCanBeReacquired(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	lock1, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("first acquireVMLock: %v", err)
	}

	lock1.Release()

	lock2, err := acquireVMLock(homeDir, "t1")
	if err != nil {
		t.Fatalf("acquireVMLock after Release: %v", err)
	}
	defer lock2.Release()
}

func TestAcquireVMLock_CreatesHomeDirIfMissing(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir() + "/does-not-exist-yet"

	lock, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("acquireVMLock: %v", err)
	}
	defer lock.Release()
}

// TestAcquireVMLock_StaleContentIsReclaimed guards the property lock.go's
// doc comment documents: the flock, not the file's body, is what enforces
// exclusion. A lock file left behind by a crashed vm-host (flock released
// by the kernel the instant that process died, per vmLock's own doc
// comment) still has its old holder's name sitting in the body until
// someone overwrites it — that stale content must never be mistaken for a
// live conflict.
func TestAcquireVMLock_StaleContentIsReclaimed(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// Simulate a crashed vm-host: a lock file whose body still names a
	// previous holder, but with no live flock on it (nothing in this test
	// ever calls syscall.Flock on this path).
	if err := os.WriteFile(filepath.Join(homeDir, vmLockFileName), []byte("dev"), 0o600); err != nil {
		t.Fatalf("seeding stale lock file: %v", err)
	}

	lock, err := acquireVMLock(homeDir, "t1")
	if err != nil {
		t.Fatalf("acquireVMLock over stale content = %v, want success (no live flock held)", err)
	}
	defer lock.Release()

	holder := readLockHolder(lock.f)
	if holder != "t1" {
		t.Errorf("lock file holder after reclaiming = %q, want %q (overwritten)", holder, "t1")
	}
}

func TestPeekVMLock_FreeWhenUnlocked(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	holder, busy, err := peekVMLock(homeDir)
	if err != nil {
		t.Fatalf("peekVMLock: %v", err)
	}

	if busy {
		t.Errorf("peekVMLock on an unlocked homeDir = busy, want free")
	}

	if holder != "" {
		t.Errorf("peekVMLock holder = %q, want empty when free", holder)
	}
}

func TestPeekVMLock_BusyReportsHolder(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	lock, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("acquireVMLock: %v", err)
	}
	defer lock.Release()

	holder, busy, err := peekVMLock(homeDir)
	if err != nil {
		t.Fatalf("peekVMLock: %v", err)
	}

	if !busy {
		t.Fatal("peekVMLock while a lock is held = free, want busy")
	}

	if holder != "dev" {
		t.Errorf("peekVMLock holder = %q, want %q", holder, "dev")
	}
}

// TestPeekVMLock_DoesNotItselfHoldLock guards peekVMLock's core contract:
// it must never become, or leave behind, a holder of its own — otherwise
// a "rask create" that merely checks for a conflict would itself start
// blocking every subsequent check and the real vm-host's own acquire.
func TestPeekVMLock_DoesNotItselfHoldLock(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	if _, busy, err := peekVMLock(homeDir); err != nil {
		t.Fatalf("peekVMLock: %v", err)
	} else if busy {
		t.Fatal("peekVMLock on a fresh homeDir = busy, want free")
	}

	// If the peek above left the lock held, this acquire would fail.
	lock, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("acquireVMLock after peekVMLock = %v, want success (peek must not hold the lock)", err)
	}
	lock.Release()
}

func TestLockConflictError_MessageContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		holder     string
		wantSubstr []string
	}{
		{
			name:       "known holder",
			holder:     "dev",
			wantSubstr: []string{"another rask VM is already running", `cluster "dev"`, "only one VM may run at a time", "delete it first"},
		},
		{
			name:       "unknown holder",
			holder:     "",
			wantSubstr: []string{"another rask VM is already running", "only one VM may run at a time", "delete it first"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := lockConflictError(tt.holder)
			if !errors.Is(err, ErrAnotherVMRunning) {
				t.Fatalf("lockConflictError(%q) = %v, want it to wrap ErrAnotherVMRunning", tt.holder, err)
			}

			for _, substr := range tt.wantSubstr {
				if !strings.Contains(err.Error(), substr) {
					t.Errorf("lockConflictError(%q) = %q, want it to contain %q", tt.holder, err.Error(), substr)
				}
			}
		})
	}
}
