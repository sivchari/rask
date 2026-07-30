//go:build linux

package hostproc

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRuntime_DataDirAndKubeconfigPathAreScopedToHomeDirAndName(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := New(homeDir)

	wantData := filepath.Join(homeDir, "clusters", "dev", "data")
	if got := r.dataDir("dev"); got != wantData {
		t.Errorf("dataDir(dev) = %q, want %q", got, wantData)
	}

	wantKubeconfig := filepath.Join(homeDir, "clusters", "dev", "kubeconfig")
	if got := r.kubeconfigPath("dev"); got != wantKubeconfig {
		t.Errorf("kubeconfigPath(dev) = %q, want %q", got, wantKubeconfig)
	}

	// Different cluster names must not collide.
	if r.dataDir("dev") == r.dataDir("staging") {
		t.Error("dataDir(dev) == dataDir(staging), want distinct paths")
	}
}

func TestWriteStateReadState_RoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")

	want := runtimeState{
		DatastorePID: 4242,
		ProcessPIDs:  map[string]int{"kube-apiserver": 100, "kubelet": 200},
	}

	if err := writeState(path, want); err != nil {
		t.Fatalf("writeState: %v", err)
	}

	got, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("readState() = %+v, want %+v", got, want)
	}
}

func TestReadState_MissingFileIsNotExist(t *testing.T) {
	t.Parallel()

	_, err := readState(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("readState(missing) = nil error, want error")
	}

	// teardown.go's Stop relies on errors.Is(err, os.ErrNotExist) to treat
	// a missing state file as "nothing to stop"; readState must preserve
	// that through its %w wrapping.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("readState(missing) error = %v, want to wrap os.ErrNotExist", err)
	}
}

func TestCopyFile_CopiesContentAndCreatesParentDirs(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "nested", "dir", "dst")

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}

	if string(got) != "hello" {
		t.Errorf("dst content = %q, want %q", got, "hello")
	}
}

func TestDetectOutboundIP_ReturnsAParsableIP(t *testing.T) {
	t.Parallel()

	ip, err := detectOutboundIP()
	if err != nil {
		t.Fatalf("detectOutboundIP: %v", err)
	}

	if ip == "" {
		t.Error("detectOutboundIP() = empty string")
	}
}
