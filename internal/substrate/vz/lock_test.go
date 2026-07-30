//go:build darwin

package vz

import (
	"errors"
	"testing"
)

func TestAcquireVMLock_SecondAcquireFails(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	lock1, err := acquireVMLock(homeDir)
	if err != nil {
		t.Fatalf("first acquireVMLock: %v", err)
	}
	defer lock1.Release()

	_, err = acquireVMLock(homeDir)
	if !errors.Is(err, ErrAnotherVMRunning) {
		t.Fatalf("second acquireVMLock error = %v, want ErrAnotherVMRunning", err)
	}
}

func TestAcquireVMLock_ReleasedLockCanBeReacquired(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	lock1, err := acquireVMLock(homeDir)
	if err != nil {
		t.Fatalf("first acquireVMLock: %v", err)
	}

	lock1.Release()

	lock2, err := acquireVMLock(homeDir)
	if err != nil {
		t.Fatalf("acquireVMLock after Release: %v", err)
	}
	defer lock2.Release()
}

func TestAcquireVMLock_CreatesHomeDirIfMissing(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir() + "/does-not-exist-yet"

	lock, err := acquireVMLock(homeDir)
	if err != nil {
		t.Fatalf("acquireVMLock: %v", err)
	}
	defer lock.Release()
}
