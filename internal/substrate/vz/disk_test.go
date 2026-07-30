//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateDataDisk_CreatesSparseFileOfRequestedSize(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data.img")

	if err := createDataDisk(path, 1); err != nil {
		t.Fatalf("createDataDisk: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if info.Size() != 1<<30 {
		t.Errorf("size = %d, want %d (1GiB)", info.Size(), int64(1)<<30)
	}
}

func TestCreateDataDisk_IdempotentWhenAlreadyExists(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data.img")

	if err := createDataDisk(path, 1); err != nil {
		t.Fatalf("createDataDisk (1st): %v", err)
	}

	if err := os.WriteFile(path, []byte("existing content, must survive"), 0o644); err != nil {
		t.Fatalf("writing marker content: %v", err)
	}

	if err := createDataDisk(path, 2); err != nil {
		t.Fatalf("createDataDisk (2nd, should be a no-op): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if string(data) != "existing content, must survive" {
		t.Error("createDataDisk overwrote an existing disk file instead of leaving it alone")
	}
}
