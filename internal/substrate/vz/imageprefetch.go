//go:build darwin

package vz

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/imagebundle"
	"github.com/sivchari/rask/internal/manifests"
)

// imageCacheDir is where internal/imagebundle caches images prefetched for
// every vz cluster on this host, alongside (but separate from) the
// component-binary cache buildTemplateInitramfs uses. Shared host-wide
// (keyed by (ref, arch), like the component cache), so Runtime.Create (this
// process) and RunVMHost (a separate process invocation — see vmhost.go)
// agree on it purely by both deriving it from the same homeDir, with no
// staging step needed between them — unlike PrebootFiles/cluster-config/
// component overrides, which are genuinely per-cluster inputs Runtime.Start
// never persists anywhere RunVMHost could otherwise find them.
func imageCacheDir(homeDir string) string {
	return filepath.Join(homeDir, "cache", "images")
}

// vzRequiredImages is every image a vz cluster's default manifest bundle
// can end up pulling: coreDNSImage plus components.PauseImage (the CRI
// sandbox image containerd's own config pins for every pod — see
// bootstrap's containerdConfigTemplate) plus manifests.RequiredImages'
// local-path-provisioner images. Identical in shape to
// internal/substrate/hostproc's own requiredImages; not shared as a common
// helper for the same reason rask-init's applyManifests doc comment gives
// for not sharing hostproc's applyManifests — arch is always
// components.ARM64 here, never components.HostArch(), so a shared helper
// would need an extra parameter for no real duplication savings.
func vzRequiredImages(coreDNSImage string) []string {
	return append([]string{components.PauseImage}, manifests.RequiredImages(coreDNSImage)...)
}

// coreDNSImageOrDefault returns img if set, else manifests.CoreDNSImage —
// the same zero-value convention substrate.StartOptions.CoreDNSImage and
// internal/guestconfig.Config.CoreDNSImage both already use.
func coreDNSImageOrDefault(img string) string {
	if img != "" {
		return img
	}

	return manifests.CoreDNSImage
}

// prefetchImages downloads every vzRequiredImages(coreDNSImage) entry not
// already cached under imageCacheDir(homeDir). Intended to run in its own
// goroutine alongside buildTemplateInitramfs (see Runtime.Create) so a cold
// image cache adds no serial time on top of the component-binary download.
//
// Pure best-effort: an error here is only logged, never returned — a
// --coredns-image override that doesn't exist (or no network at all) must
// never fail Create. A cluster with no prefetched images simply falls back
// to kubelet pulling them live once scheduled, exactly as if this didn't
// exist. Mirrors internal/substrate/hostproc.Runtime.Create's identical
// contract for the same reason.
func prefetchImages(ctx context.Context, homeDir, coreDNSImage string) {
	refs := vzRequiredImages(coreDNSImageOrDefault(coreDNSImage))

	if _, err := imagebundle.NewCache(imageCacheDir(homeDir)).EnsureAll(ctx, refs, components.ARM64); err != nil {
		fmt.Fprintf(os.Stderr, "vz: prefetching cluster images (falling back to a live pull on demand): %v\n", err)
	}
}

// buildImagesOverlayCpio returns a cpio archive embedding every
// vzRequiredImages(coreDNSImage) entry prefetchImages already cached for
// this host, under guestlayout.ImagesDir, for rask-init to import into the
// guest's own containerd (see cmd/rask-init's images.go). A ref with no
// cache entry (prefetchImages never ran, or its own fetch for that ref
// failed) is silently skipped, matching hostproc's importCachedImages' same
// best-effort contract — the guest simply falls back to a live pull for
// that one image. Returns a zero-length (but non-nil) slice if nothing is
// cached at all, mirroring every other overlay builder's contract (see
// buildPrebootCpio).
//
// Deliberately a separate, per-cluster cpio concatenated onto the shared
// template initramfs (see vz.go's Start) rather than baked into
// buildTemplateInitramfs itself: the template is unpacked into guest RAM on
// every boot, and it's shared host-wide across every cluster, so adding
// image archives to it would pay their RAM/unpack cost on every single
// cluster boot — including ones that never schedule the pod that needs a
// given image — instead of once per host, on demand, the way this overlay
// (built fresh from whatever prefetchImages already cached) does.
func buildImagesOverlayCpio(homeDir, coreDNSImage string) ([]byte, error) {
	cache := imagebundle.NewCache(imageCacheDir(homeDir))
	guestPrefix := strings.TrimPrefix(guestlayout.ImagesDir, "/")

	w := newCpioWriter()
	wrote := false

	for _, ref := range vzRequiredImages(coreDNSImageOrDefault(coreDNSImage)) {
		archivePath, ok := cache.Lookup(ref, components.ARM64)
		if !ok {
			continue
		}

		data, err := os.ReadFile(archivePath)
		if err != nil {
			return nil, fmt.Errorf("vz: reading cached image archive %s: %w", archivePath, err)
		}

		if err := w.WriteFile(path.Join(guestPrefix, filepath.Base(archivePath)), 0o644, data); err != nil {
			return nil, fmt.Errorf("vz: writing image archive %s into overlay: %w", filepath.Base(archivePath), err)
		}

		wrote = true
	}

	if !wrote {
		return []byte{}, nil
	}

	return w.Finish()
}
