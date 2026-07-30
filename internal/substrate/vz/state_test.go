//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadVMState_RoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "vm-state.json")
	want := vmState{PID: 4242, HostPort: 51000, AgentHostPort: 51001}

	if err := writeVMState(path, want); err != nil {
		t.Fatalf("writeVMState: %v", err)
	}

	got, err := readVMState(path)
	if err != nil {
		t.Fatalf("readVMState: %v", err)
	}

	if got != want {
		t.Errorf("readVMState = %+v, want %+v", got, want)
	}
}

func TestReadVMState_MissingFile(t *testing.T) {
	t.Parallel()

	if _, err := readVMState(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("readVMState on a missing file = nil error, want error")
	}
}

func TestVMStatePath_UnderClusterDataDir(t *testing.T) {
	t.Parallel()

	got := vmStatePath("/home/.rask", "dev")
	want := filepath.Join("/home/.rask", "clusters", "dev", "data", "vm-state.json")

	if got != want {
		t.Errorf("vmStatePath = %q, want %q", got, want)
	}
}

func TestWriteVMState_AtomicRenameLeavesNoTmpFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vm-state.json")

	if err := writeVMState(path, vmState{PID: 1}); err != nil {
		t.Fatalf("writeVMState: %v", err)
	}

	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temp file was left behind after writeVMState")
	}
}
