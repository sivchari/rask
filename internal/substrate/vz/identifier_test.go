//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateMachineIdentifier_CreatesAndPersists(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "machine-id")

	if _, err := os.Stat(path); err == nil {
		t.Fatal("identifier file should not exist yet")
	}

	data1, err := loadOrCreateMachineIdentifierBytes(path)
	if err != nil {
		t.Fatalf("loadOrCreateMachineIdentifierBytes (create): %v", err)
	}

	if len(data1) == 0 {
		t.Fatal("created identifier is empty")
	}

	data2, err := loadOrCreateMachineIdentifierBytes(path)
	if err != nil {
		t.Fatalf("loadOrCreateMachineIdentifierBytes (load): %v", err)
	}

	if string(data1) != string(data2) {
		t.Error("second call returned different bytes than the first; identifier is not stable across calls")
	}
}
