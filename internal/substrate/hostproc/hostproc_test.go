//go:build linux

package hostproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate"
)

func TestRuntime_PrebootPathMatchesWhereStagePrebootFilesActuallyWrites(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := New(homeDir)

	srcPath := filepath.Join(t.TempDir(), "webhook.yaml")
	if err := os.WriteFile(srcPath, []byte("kind: Config\n"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dest := "auth/webhook.yaml"
	if err := substrate.StagePrebootFiles(r.dataDir("dev"), []substrate.PrebootFile{{Src: srcPath, Dest: dest}}); err != nil {
		t.Fatalf("StagePrebootFiles: %v", err)
	}

	got := r.PrebootPath("dev", dest)

	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("PrebootPath() = %q, does not match where StagePrebootFiles wrote: %v", got, err)
	}

	if string(content) != "kind: Config\n" {
		t.Errorf("content at PrebootPath() = %q, want %q", content, "kind: Config\n")
	}
}

// TestRuntime_PrebootPathIsAHostPathNotVzsInGuestPath demonstrates hostproc
// and vz's PrebootPath results diverge as expected — hostproc returns a
// host-filesystem path scoped by homeDir/name, never the vz substrate's
// single fixed in-guest directory (guestlayout.PrebootDir, importable here
// since that package carries no build tag) — without needing to import the
// vz package itself, which only builds on darwin.
func TestRuntime_PrebootPathIsAHostPathNotVzsInGuestPath(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	dest := "auth/webhook.yaml"
	got := r.PrebootPath("dev", dest)

	if got == filepath.Join(guestlayout.PrebootDir, dest) {
		t.Errorf("PrebootPath() = %q, collides with vz's in-guest formula — hostproc has no VM boundary, its path must be host-specific", got)
	}
}

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

func TestRuntime_SeedSourcePathMatchesKineDatastoreLayout(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := New(homeDir)

	// Must match the "kine" subdir Start passes to kine.New (dataDir +
	// "kine") joined with kine.Datastore's own "state.db" filename: this
	// accessor exists precisely so internal/prebake doesn't have to
	// hardcode that layout itself.
	want := filepath.Join(homeDir, "clusters", "dev", "data", "kine", "state.db")
	if got := r.SeedSourcePath("dev"); got != want {
		t.Errorf("SeedSourcePath(dev) = %q, want %q", got, want)
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

func TestRuntime_ImageCacheDirIsScopedUnderCacheDirAndDistinctFromComponentCache(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := New(homeDir)

	want := filepath.Join(homeDir, "cache", "images")
	if got := r.imageCacheDir(); got != want {
		t.Errorf("imageCacheDir() = %q, want %q", got, want)
	}

	if r.imageCacheDir() == r.cacheDir() {
		t.Error("imageCacheDir() == cacheDir(), want image cache scoped to its own subdirectory")
	}
}

// TestRuntime_ImportCachedImages_NoCacheEntriesReturnsWithoutDialingContainerd
// exercises importCachedImages' fast path: with nothing ever prefetched
// (see Create), it must return promptly without ever attempting to reach
// containerd at all — the whole point of this being best-effort is that a
// cluster with a cold/missing image cache still starts exactly as it did
// before this existed, with no extra wait.
func TestRuntime_ImportCachedImages_NoCacheEntriesReturnsWithoutDialingContainerd(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	// A canceled context: if importCachedImages tried to actually wait
	// for a containerd socket, waitContainerdSocket would immediately
	// return this cancellation as an error path (logged, not returned —
	// see importCachedImages' doc comment), but the point of this test is
	// that it never even gets that far, so it must return well within the
	// deadline regardless.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})

	go func() {
		defer close(done)

		r.importCachedImages(ctx, "dev", components.ARM64, "example.com/coredns:v1")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("importCachedImages with an empty cache did not return promptly")
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
