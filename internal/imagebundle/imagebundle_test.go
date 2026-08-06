package imagebundle

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
	"github.com/sivchari/rask/internal/manifests"
)

// newTestRegistry starts an in-process, network-free OCI registry (no real
// network access, matching the "verify without an internet dependency"
// bar for a unit test) and pushes a single random image to it, returning
// the image's pullable "<host>/<repo>:tag" reference.
func newTestRegistry(t *testing.T) string {
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

func TestCache_Lookup_AbsentByDefault(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	if _, ok := c.Lookup("example.com/some/image:v1", components.ARM64); ok {
		t.Error("Lookup() on an empty cache = ok, want not found")
	}
}

func TestCache_Ensure_CachesAndLookupThenFindsIt(t *testing.T) {
	t.Parallel()

	ref := newTestRegistry(t)
	c := NewCache(t.TempDir())

	path, err := c.Ensure(context.Background(), ref, components.ARM64)
	if err != nil {
		t.Fatalf("Ensure(%q): %v", ref, err)
	}

	if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
		t.Fatalf("Ensure() cached archive at %s: stat err=%v, size check failed", path, statErr)
	}

	gotPath, ok := c.Lookup(ref, components.ARM64)
	if !ok {
		t.Fatal("Lookup() after Ensure() = not found, want found")
	}

	if gotPath != path {
		t.Errorf("Lookup() = %q, want %q (Ensure()'s own return value)", gotPath, path)
	}
}

func TestCache_Ensure_SecondCallIsCacheHitAndNeedsNoNetwork(t *testing.T) {
	t.Parallel()

	ref := newTestRegistry(t)
	c := NewCache(t.TempDir())

	if _, err := c.Ensure(context.Background(), ref, components.ARM64); err != nil {
		t.Fatalf("first Ensure(%q): %v", ref, err)
	}

	path := c.archivePath(ref, components.ARM64)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cached archive: %v", err)
	}

	// A canceled context would fail any call that actually tries to hit
	// the network; a second Ensure() call must be a pure cache hit and
	// therefore succeed despite it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := c.Ensure(ctx, ref, components.ARM64)
	if err != nil {
		t.Fatalf("second Ensure(%q) with a canceled context: %v (want a pure cache hit, no network)", ref, err)
	}

	if got != path {
		t.Errorf("second Ensure() = %q, want %q", got, path)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cached archive after second Ensure(): %v", err)
	}

	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("second Ensure() rewrote the cached archive, want it untouched")
	}
}

func TestCache_Ensure_DifferentArchesAreCachedSeparately(t *testing.T) {
	t.Parallel()

	ref := newTestRegistry(t)
	c := NewCache(t.TempDir())

	armPath, err := c.Ensure(context.Background(), ref, components.ARM64)
	if err != nil {
		t.Fatalf("Ensure(arm64): %v", err)
	}

	amdPath, err := c.Ensure(context.Background(), ref, components.AMD64)
	if err != nil {
		t.Fatalf("Ensure(amd64): %v", err)
	}

	if armPath == amdPath {
		t.Errorf("Ensure() cached arm64 and amd64 at the same path %q, want distinct paths", armPath)
	}

	if _, ok := c.Lookup(ref, components.ARM64); !ok {
		t.Error("Lookup(arm64) after both Ensure() calls = not found")
	}

	if _, ok := c.Lookup(ref, components.AMD64); !ok {
		t.Error("Lookup(amd64) after both Ensure() calls = not found")
	}
}

func TestCache_Ensure_InvalidRefErrors(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	if _, err := c.Ensure(context.Background(), "127.0.0.1:0/does-not-exist:v1", components.ARM64); err == nil {
		t.Fatal("Ensure() on an unreachable registry = nil error, want error")
	}
}

func TestCache_EnsureAll_ReturnsPathsInInputOrder(t *testing.T) {
	t.Parallel()

	ref1 := newTestRegistry(t)
	ref2 := newTestRegistry(t)
	c := NewCache(t.TempDir())

	paths, err := c.EnsureAll(context.Background(), []string{ref1, ref2}, components.ARM64)
	if err != nil {
		t.Fatalf("EnsureAll(): %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("EnsureAll() returned %d paths, want 2", len(paths))
	}

	if want := c.archivePath(ref1, components.ARM64); paths[0] != want {
		t.Errorf("EnsureAll()[0] = %q, want %q", paths[0], want)
	}

	if want := c.archivePath(ref2, components.ARM64); paths[1] != want {
		t.Errorf("EnsureAll()[1] = %q, want %q", paths[1], want)
	}
}

func TestCache_EnsureAll_OneFailureFailsTheWhole(t *testing.T) {
	t.Parallel()

	ref := newTestRegistry(t)
	c := NewCache(t.TempDir())

	_, err := c.EnsureAll(context.Background(), []string{ref, "127.0.0.1:0/does-not-exist:v1"}, components.ARM64)
	if err == nil {
		t.Fatal("EnsureAll() with one unreachable ref = nil error, want error")
	}
}

func TestArchivePath_IsHumanReadableAndArchScoped(t *testing.T) {
	t.Parallel()

	c := NewCache("/cache")

	got := c.archivePath("registry.k8s.io/pause:3.10", components.ARM64)
	want := filepath.Join("/cache", "arm64", "registry.k8s.io_pause_3.10.tar")

	if got != want {
		t.Errorf("archivePath() = %q, want %q", got, want)
	}
}

// TestArchivePath_DiffersWhenRefChanges guards the cache-key contract
// RequiredImages depends on: a bundled payload is staged under
// archivePath's exact filename (see cmd/bundle-payload and
// bundlepayload.InstallImages), so a version bump that changes an image's
// tag (e.g. manifests.CoreDNSImage) must produce a different cache entry,
// never silently reuse a stale one.
func TestArchivePath_DiffersWhenRefChanges(t *testing.T) {
	t.Parallel()

	c := NewCache("/cache")

	older := c.archivePath("registry.k8s.io/coredns/coredns:v1.14.5", components.ARM64)
	newer := c.archivePath("registry.k8s.io/coredns/coredns:v1.14.6", components.ARM64)

	if older == newer {
		t.Errorf("archivePath() did not change when the image tag changed: both = %q", older)
	}
}

func TestRequiredImages_IncludesPauseAndDefaultCoreDNSImage(t *testing.T) {
	t.Parallel()

	refs := RequiredImages()

	if len(refs) == 0 || refs[0] != components.PauseImage {
		t.Fatalf("RequiredImages()[0] = %v, want the pause image %q first", refs, components.PauseImage)
	}

	found := false

	for _, r := range refs {
		if r == manifests.CoreDNSImage {
			found = true
		}
	}

	if !found {
		t.Errorf("RequiredImages() = %v, want it to include manifests.CoreDNSImage %q", refs, manifests.CoreDNSImage)
	}
}

// TestCache_Ensure_PayloadInstallSatisfiesRefWithoutNetwork proves the
// "payload before network" contract Lookup's installFromPayload
// implements: with a fake payload installer that stages an unroutable
// ref's archive directly, Ensure must succeed without ever attempting a
// live crane.Pull (which would fail loudly against 127.0.0.1:0). Not run
// in parallel: it mutates the package-level installImagesFromPayload var,
// which every other test in this file also reads through Lookup/Ensure —
// see installImagesFromPayload's own doc comment for why a var exists at
// all.
func TestCache_Ensure_PayloadInstallSatisfiesRefWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	ref := "127.0.0.1:0/does-not-exist:v1"

	orig := installImagesFromPayload
	t.Cleanup(func() { installImagesFromPayload = orig })

	installImagesFromPayload = func(destDir string) error {
		archive := filepath.Join(destDir, string(components.ARM64), sanitizeRef(ref)+".tar")
		if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
			return err
		}

		return os.WriteFile(archive, []byte("staged payload archive"), 0o644)
	}

	c := NewCache(dir)

	got, err := c.Ensure(context.Background(), ref, components.ARM64)
	if err != nil {
		t.Fatalf("Ensure() with a payload-staged ref = %v, want success with no network pull", err)
	}

	if want := c.archivePath(ref, components.ARM64); got != want {
		t.Errorf("Ensure() = %q, want %q", got, want)
	}
}

// TestCache_Ensure_FallsBackToNetworkWhenPayloadHasNoImage documents the
// non-bundled-build contract: installImagesFromPayload defaults to
// bundlepayload.InstallImages, which is a no-op against the empty embed a
// plain (non "-tags bundle") test binary always compiles with (see
// bundlepayload's own TestInstallImages_NoOpWithoutPayload) — Ensure must
// still resolve ref by pulling it live, exactly as before this package
// gained any payload-awareness.
func TestCache_Ensure_FallsBackToNetworkWhenPayloadHasNoImage(t *testing.T) {
	t.Parallel()

	ref := newTestRegistry(t)
	c := NewCache(t.TempDir())

	path, err := c.Ensure(context.Background(), ref, components.ARM64)
	if err != nil {
		t.Fatalf("Ensure() with no payload-staged image = %v, want a live pull to succeed", err)
	}

	if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
		t.Fatalf("Ensure() cached archive at %s: stat err=%v", path, statErr)
	}
}
