// Package imagebundle prefetches and caches the container images a rask
// cluster's default manifest bundle needs (see
// internal/manifests.RequiredImages and internal/components.PauseImage),
// pure-Go and without a container daemon, via google/go-containerregistry's
// crane package.
//
// Images are cached as docker-save style tar archives — the same archive
// format internal/substrate.Runtime's LoadImages already knows how to
// import (see internal/substrate/hostproc/loadimages.go's doc comment) —
// under a filename derived from the image reference, so a later Lookup for
// an already-cached (ref, arch) pair is a pure local disk check with no
// network round trip at all. This is what lets a substrate's Start import
// a cluster's images from local disk instead of kubelet/containerd pulling
// them from a registry once the corresponding pod is scheduled.
//
// On a bundled binary, Cache additionally resolves a ref from this build's
// embedded payload before ever touching the network (see Cache.Lookup's
// installFromPayload) — cmd/bundle-payload stages the RequiredImages set
// into that payload via this same Cache, so a bundled "rask pull" followed
// by "rask create" needs no registry access at all.
package imagebundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/sync/errgroup"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/components/bundlepayload"
	"github.com/sivchari/rask/internal/manifests"
)

// installImagesFromPayload is a package variable, not a direct call to
// bundlepayload.InstallImages, so this package's own tests can substitute a
// fake payload installer: a real embedded payload only exists in a binary
// compiled with `go build -tags bundle` (see bundlepayload's package doc),
// which unit tests never do, so there is no other way to exercise "an image
// already staged in the payload is found without a network pull" here.
var installImagesFromPayload = bundlepayload.InstallImages

// RequiredImages returns every container image reference cmd/bundle-payload
// stages into a bundled binary's payload for the default cluster (no
// --coredns-image override): components.PauseImage (the CRI sandbox image
// containerd's own config pins for every pod) plus
// manifests.RequiredImages(manifests.CoreDNSImage). This is the one place
// that composes those pins into the ordered ref list actually pulled/
// staged; a version bump to any of them (components.PauseImage,
// manifests.CoreDNSImage, or local-path-storage.yaml's own pinned images)
// changes what this returns with no second place to update.
//
// internal/substrate/hostproc and internal/substrate/vz build the same list
// themselves (their own requiredImages/vzRequiredImages), parameterized by
// a possible runtime --coredns-image override this function doesn't need to
// support.
func RequiredImages() []string {
	return append([]string{components.PauseImage}, manifests.RequiredImages(manifests.CoreDNSImage)...)
}

// Cache downloads and caches container images, as docker-save style tar
// archives, under an architecture-scoped directory tree. A Cache is safe
// for concurrent use.
type Cache struct {
	dir string

	// installPayloadOnce guards installFromPayload: every Lookup/Ensure
	// call goes through it, but the embedded payload only needs copying
	// into dir once per Cache instance.
	installPayloadOnce sync.Once
}

// NewCache returns a Cache rooted at dir (typically
// ~/.rask/cache/images).
func NewCache(dir string) *Cache {
	return &Cache{dir: dir}
}

// archivePath returns the path Ensure caches ref's (arch's) archive at. The
// filename is derived from ref itself, not its digest: Lookup and Ensure
// only ever check local disk for an existing archive under this name, so a
// ref that starts resolving to a new digest upstream keeps being served
// whatever was cached the first time, until the cache directory is cleared
// — the same "cached forever until evicted" contract
// internal/components.Cache already uses for pinned component binaries.
func (c *Cache) archivePath(ref string, arch components.Arch) string {
	return filepath.Join(c.dir, string(arch), sanitizeRef(ref)+".tar")
}

// sanitizeRef replaces every character in ref that isn't safe in a single
// path segment with '_', keeping the result human-readable for debugging —
// e.g. ~/.rask/cache/images/arm64/registry.k8s.io_pause_3.10.tar.
func sanitizeRef(ref string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, ref)
}

// Lookup returns ref's cached archive path for arch and true if it is
// already present, without ever fetching it over the network. Callers that
// must stay off the network on their own critical path (e.g. a substrate's
// Start, see internal/substrate/hostproc's importCachedImages) use this
// instead of Ensure; prefetching (Ensure/EnsureAll) is expected to have
// already run earlier (typically during Create).
//
// Before checking disk, it installs this build's embedded payload images
// (if any — see installFromPayload) into dir, so a ref the payload already
// staged is found here too, not just via Ensure. This is local, in-memory
// to-disk copying, never network I/O, so it doesn't violate Lookup's own
// "without ever fetching it" contract.
func (c *Cache) Lookup(ref string, arch components.Arch) (string, bool) {
	c.installFromPayload()

	path := c.archivePath(ref, arch)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}

	return path, true
}

// installFromPayload copies every container-image archive this build's
// embedded payload staged (see bundlepayload.InstallImages) into c.dir,
// once per Cache instance, so a later Lookup/Ensure call for any of those
// refs is a pure disk hit with no network round trip at all — mirroring how
// components.DefaultCache already prefers the embedded payload over the
// network for component binaries (see that function's doc comment). A
// container image pull is a multi-request OCI registry exchange, not a
// single URL fetch, so there is no shared http.RoundTripper seam to
// intercept the way DefaultCache does; pre-populating the cache directory
// once, up front, is the equivalent for crane's pull path.
//
// A no-op on a build with no embedded payload, or on a payload that never
// staged any images: bundlepayload.InstallImages already handles both
// cases by doing nothing and returning nil. Any other error is silently
// ignored — Ensure's normal network pull is the existing fallback for
// whatever ref is still missing, exactly as if this didn't run at all.
func (c *Cache) installFromPayload() {
	c.installPayloadOnce.Do(func() {
		_ = installImagesFromPayload(c.dir)
	})
}

// Ensure fetches ref for arch into the cache if not already present, and
// returns the path to the resulting docker-save style tar archive. A
// second Ensure call for the same (ref, arch) is a pure local disk check
// with no network round trip.
func (c *Cache) Ensure(ctx context.Context, ref string, arch components.Arch) (string, error) {
	if path, ok := c.Lookup(ref, arch); ok {
		return path, nil
	}

	dest := c.archivePath(ref, arch)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("imagebundle: creating cache dir: %w", err)
	}

	img, err := crane.Pull(ref, crane.WithContext(ctx), crane.WithPlatform(&v1.Platform{Architecture: string(arch), OS: "linux"}))
	if err != nil {
		return "", fmt.Errorf("imagebundle: pulling %s (%s): %w", ref, arch, err)
	}

	// A unique per-call temp filename (rather than a single fixed
	// "dest+.tmp"): two concurrent Ensure calls for the same (ref, arch)
	// — possible via EnsureAll, if a caller ever passes a duplicate ref —
	// would otherwise both write through the same temp path at once and
	// corrupt each other's output.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("imagebundle: creating temp file: %w", err)
	}

	tmpPath := tmp.Name()
	_ = tmp.Close()

	defer func() { _ = os.Remove(tmpPath) }()

	if err := crane.Save(img, ref, tmpPath); err != nil {
		return "", fmt.Errorf("imagebundle: saving %s: %w", ref, err)
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("imagebundle: finalizing %s: %w", dest, err)
	}

	return dest, nil
}

// EnsureAll calls Ensure for every ref in refs concurrently, and returns
// their cached archive paths in the same order as refs.
func (c *Cache) EnsureAll(ctx context.Context, refs []string, arch components.Arch) ([]string, error) {
	paths := make([]string, len(refs))

	g, gctx := errgroup.WithContext(ctx)

	for i, ref := range refs {
		g.Go(func() error {
			path, err := c.Ensure(gctx, ref, arch)
			if err != nil {
				return err
			}

			paths[i] = path

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return paths, nil
}
