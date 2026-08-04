//go:build darwin

package vz

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/imagebundle"
)

// newTestImageRegistry starts an in-process, network-free OCI registry
// (matches internal/imagebundle's own test helper) and pushes a single
// random image to it, returning a pullable "<host>/<repo>:tag" reference.
func newTestImageRegistry(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}

	ref := strings.TrimPrefix(srv.URL, "http://") + "/test/image:v1"

	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("crane.Push(%q): %v", ref, err)
	}

	return ref
}

func TestCoreDNSImageOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		img  string
		want string
	}{
		{"empty falls back to the default", "", "registry.k8s.io/coredns/coredns:v1.14.6"},
		{"explicit override wins", "example.com/coredns:v1", "example.com/coredns:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := coreDNSImageOrDefault(tt.img); got != tt.want {
				t.Errorf("coreDNSImageOrDefault(%q) = %q, want %q", tt.img, got, tt.want)
			}
		})
	}
}

func TestVzRequiredImages_IncludesPauseAndCoreDNS(t *testing.T) {
	t.Parallel()

	refs := vzRequiredImages("example.com/coredns:v1")

	if refs[0] != components.PauseImage {
		t.Errorf("vzRequiredImages()[0] = %q, want the pause image %q first", refs[0], components.PauseImage)
	}

	found := false

	for _, r := range refs {
		if r == "example.com/coredns:v1" {
			found = true
		}
	}

	if !found {
		t.Errorf("vzRequiredImages() = %v, want it to include the requested coredns image", refs)
	}
}

func TestBuildImagesOverlayCpio_NothingCachedReturnsEmptyArchive(t *testing.T) {
	t.Parallel()

	data, err := buildImagesOverlayCpio(t.TempDir(), "")
	if err != nil {
		t.Fatalf("buildImagesOverlayCpio: %v", err)
	}

	if len(data) != 0 {
		t.Errorf("buildImagesOverlayCpio() with nothing cached = %d bytes, want 0", len(data))
	}
}

func TestBuildImagesOverlayCpio_EmbedsACachedArchive(t *testing.T) {
	t.Parallel()

	ref := newTestImageRegistry(t)
	homeDir := t.TempDir()

	// Prime the shared image cache directly (bypassing vzRequiredImages'
	// fixed coredns/pause/local-path ref list, which this local test
	// registry can't serve): buildImagesOverlayCpio only cares that
	// *something* vzRequiredImages("") asks for is already cached, so
	// override coreDNSImage to ref itself.
	cache := imagebundle.NewCache(imageCacheDir(homeDir))
	if _, err := cache.Ensure(context.Background(), ref, components.ARM64); err != nil {
		t.Fatalf("priming image cache: %v", err)
	}

	data, err := buildImagesOverlayCpio(homeDir, ref)
	if err != nil {
		t.Fatalf("buildImagesOverlayCpio: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("buildImagesOverlayCpio() with a cached image = empty archive, want non-empty")
	}

	extracted := extractArchiveForTest(t, data)

	archivePath, ok := cache.Lookup(ref, components.ARM64)
	if !ok {
		t.Fatal("cache.Lookup() after Ensure() = not found")
	}

	guestPath := filepath.Join(extracted, strings.TrimPrefix(guestlayout.ImagesDir, "/"), filepath.Base(archivePath))

	got, err := os.ReadFile(guestPath)
	if err != nil {
		t.Fatalf("reading extracted image archive: %v", err)
	}

	want, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("reading cached image archive: %v", err)
	}

	if string(got) != string(want) {
		t.Error("extracted image archive content does not match the cached archive's content")
	}
}

// prefetchImages itself is deliberately not unit tested against a real
// registry: vzRequiredImages always includes fixed registry.k8s.io/
// local-path-provisioner refs that only a real network (or a much larger
// httptest-mirror-every-ref setup than warranted here) could resolve, and
// its own contract is "best-effort, errors only logged" — the same reason
// internal/substrate/hostproc's own Create() image-prefetch goroutine has
// no dedicated unit test either. buildImagesOverlayCpio (prefetchImages'
// only consumer) is fully covered above by priming the cache directly, and
// prefetchImages' actual network behavior is exercised by the
// --wait coredns E2E timing run (see PROGRESS-vz-seams.md).
