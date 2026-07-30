//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
const vmLockFileName = "vm.lock"

// ErrAnotherVMRunning is returned by acquireVMLock when another vz VM
// already holds the host-wide lock.
var ErrAnotherVMRunning = errors.New("vz: another rask VM is already running on this host (only one is allowed at a time)")

// vmLock holds the host-wide VM lock for as long as the process that
// acquired it keeps running. The underlying flock is released
// automatically by the kernel if the holding process exits for any reason
// (including SIGKILL), so a crashed or killed vm-host can never leave the
// lock stuck held.
type vmLock struct {
	f *os.File
}

// acquireVMLock takes an exclusive, non-blocking lock on
// homeDir/vm.lock. Must be called by the long-lived vm-host process
// itself (see vmhost.go's RunVMHost), not the short-lived "rask create"
// CLI process that spawns it: the lock needs to be held for as long as the
// VM is running, not just for the duration of one CLI invocation.
func acquireVMLock(homeDir string) (*vmLock, error) {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return nil, fmt.Errorf("vz: creating %s: %w", homeDir, err)
	}

	path := filepath.Join(homeDir, vmLockFileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vz: opening %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAnotherVMRunning
		}

		return nil, fmt.Errorf("vz: locking %s: %w", path, err)
	}

	return &vmLock{f: f}, nil
}

// Release drops the lock and closes the underlying file.
func (l *vmLock) Release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
