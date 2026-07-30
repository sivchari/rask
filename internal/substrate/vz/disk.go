//go:build darwin

package vz

import (
	"fmt"
	"os"
)

// createDataDisk creates a sparse raw disk file at path of size sizeGB
// GiB, for the guest's virtio-blk data disk (guestlayout.DataDiskDevice).
// A no-op if path already exists — Create is expected to be safe to retry.
func createDataDisk(path string, sizeGB int64) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("vz: creating data disk %s: %w", path, err)
	}

	if err := f.Truncate(sizeGB << 30); err != nil {
		_ = f.Close()

		return fmt.Errorf("vz: sizing data disk %s to %dGiB: %w", path, sizeGB, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("vz: closing data disk %s: %w", path, err)
	}

	return nil
}
