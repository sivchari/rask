//go:build linux

package hostproc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/substrate"
)

func TestRuntime_ContainerdSocketPathMatchesBootstrapLayout(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	// Must match internal/bootstrap/config.go's writeContainerdConfig
	// layout (dataDir/containerd/containerd.sock): LoadImages dials this
	// exact socket, with no forwarding or translation, since hostproc has
	// no VM boundary.
	want := filepath.Join(r.dataDir("dev"), "containerd", "containerd.sock")
	if got := r.containerdSocketPath("dev"); got != want {
		t.Errorf("containerdSocketPath(dev) = %q, want %q", got, want)
	}
}

func TestRuntime_LoadImagesOnNotRunningClusterErrorsWithoutDialing(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())
	name := "dev"

	if err := os.MkdirAll(r.dataDir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// No running marker written (mirrors a cluster that was never started,
	// or was already Stopped): LoadImages must reject this before ever
	// attempting to dial the (nonexistent) containerd socket.
	err := r.LoadImages(context.Background(), name, []substrate.ImageSource{
		{Reference: "busybox:1.36", Stream: strings.NewReader("unused")},
	})
	if err == nil {
		t.Fatal("LoadImages() on a not-running cluster = nil error, want error")
	}
}

func TestRuntime_LoadImagesOnUnknownClusterErrors(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	err := r.LoadImages(context.Background(), "does-not-exist", []substrate.ImageSource{
		{Reference: "busybox:1.36", Stream: strings.NewReader("unused")},
	})
	if err == nil {
		t.Fatal("LoadImages() on an unknown cluster = nil error, want error")
	}
}
