//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// vmLockFileName is the host-wide advisory lock every vm-host process
// holds for its entire lifetime, preventing more than one vz VM from
// running at a time on this host.
//
// Deliberately global rather than per-cluster: added after a real host
// instability incident during E2E testing on this exact machine (repeated
// system-wide crashes while a VM was running), and there is not yet
// evidence that multiple concurrent VMs are safe on this substrate.
// Conservative until proven otherwise.
//
// Whichever process holds the flock also writes its cluster name into the
// file's body (writeLockHolder/readLockHolder below) — purely
// informational, so a losing acquireVMLock/peekVMLock caller can name the
// holder in its error message. The flock itself, not this content, is
// what enforces exclusion: a torn or stale body can never let two VMs run
// at once, worst case a losing caller's error just can't name the holder.
const vmLockFileName = "vm.lock"

// ErrAnotherVMRunning is returned by acquireVMLock and peekVMLock (via
// lockConflictError) when another vz VM already holds the host-wide lock.
var ErrAnotherVMRunning = errors.New("vz: another rask VM is already running")

// vmLock holds the host-wide VM lock for as long as the process that
// acquired it keeps running. The underlying flock is released
// automatically by the kernel if the holding process exits for any reason
// (including SIGKILL), so a crashed or killed vm-host can never leave the
// lock stuck held.
type vmLock struct {
	f *os.File
}

// acquireVMLock takes an exclusive, non-blocking lock on
// homeDir/vm.lock and records clusterName as its holder (readLockHolder).
// Must be called by the long-lived vm-host process itself (see
// vmhost.go's RunVMHost), not the short-lived "rask create" CLI process
// that spawns it: the lock needs to be held for as long as the VM is
// running, not just for the duration of one CLI invocation.
//
// A losing acquire never leaves this call blocked: a stuck lock would be
// exactly the "hangs for minutes with no explanation" failure mode this
// was written to prevent (see Runtime.Start's own peekVMLock check, which
// fails a "rask create" this fast before ever spawning a vm-host process
// for a doomed VM).
func acquireVMLock(homeDir, clusterName string) (*vmLock, error) {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return nil, fmt.Errorf("vz: creating %s: %w", homeDir, err)
	}

	path := filepath.Join(homeDir, vmLockFileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vz: opening %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer func() { _ = f.Close() }()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, lockConflictError(readLockHolder(f))
		}

		return nil, fmt.Errorf("vz: locking %s: %w", path, err)
	}

	if err := writeLockHolder(f, clusterName); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()

		return nil, fmt.Errorf("vz: recording lock holder in %s: %w", path, err)
	}

	return &vmLock{f: f}, nil
}

// Release drops the lock and closes the underlying file.
func (l *vmLock) Release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// peekVMLock reports whether the host-wide VM lock is currently held by
// someone else, without itself taking or holding it: unlike acquireVMLock
// (which must be called by the long-lived vm-host process and holds the
// lock for as long as the VM runs), this is for Runtime.Start's short-lived
// CLI process to fail fast — before spawning a vm-host process at all —
// when a VM is already running elsewhere on the host. It opens its own fd
// and takes-then-immediately-releases a non-blocking flock, so it never
// itself becomes, or interferes with, the actual holder.
//
// A "free" result here is inherently racy (TOCTOU: another "rask create"
// could acquire the real lock between this returning and this process's
// own vm-host spawning) — that's fine, this is an optimization to fail
// fast in the overwhelmingly common case (a VM already running when this
// is called), not the sole enforcement mechanism: the real exclusion is
// still acquireVMLock's own flock inside vm-host, whose failure
// Runtime.waitForVMState already handles by detecting the vm-host
// process's fast exit.
func peekVMLock(homeDir string) (holder string, busy bool, err error) {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return "", false, fmt.Errorf("vz: creating %s: %w", homeDir, err)
	}

	path := filepath.Join(homeDir, vmLockFileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("vz: opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return readLockHolder(f), true, nil
		}

		return "", false, fmt.Errorf("vz: checking %s: %w", path, err)
	}

	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return "", false, nil
}

// lockConflictError builds the error a losing acquireVMLock/peekVMLock
// caller returns, naming holder when known.
func lockConflictError(holder string) error {
	if holder == "" {
		return fmt.Errorf("%w; only one VM may run at a time — delete it first", ErrAnotherVMRunning)
	}

	return fmt.Errorf("%w (cluster %q); only one VM may run at a time — delete it first", ErrAnotherVMRunning, holder)
}

// writeLockHolder overwrites f's entire contents with clusterName. f must
// be positioned at (or seekable back to) offset 0 and already hold the
// flock — see acquireVMLock, its only caller.
func writeLockHolder(f *os.File, clusterName string) error {
	if err := f.Truncate(0); err != nil {
		return err
	}

	if _, err := f.WriteAt([]byte(clusterName), 0); err != nil {
		return err
	}

	return nil
}

// readLockHolder best-effort reads back the cluster name a previous
// writeLockHolder call recorded in f. Returns "" (unknown holder) on any
// read failure — this is purely for a nicer error message, never load-
// bearing for correctness, so a read error here must never itself become
// a fatal error.
func readLockHolder(f *os.File) string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}
