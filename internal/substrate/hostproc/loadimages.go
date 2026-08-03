//go:build linux

package hostproc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/platforms"

	"github.com/sivchari/rask/internal/substrate"
)

// importPlatform restricts LoadImages to importing only the host's own
// platform out of an archive's manifest. "docker save" (and "docker
// buildx"-built images generally) exports the image's full multi-arch
// index — every platform variant Docker knows about, e.g. amd64, arm/v5,
// arm/v6, arm64, ... — but the archive's blobs directory only actually
// contains the blobs for whichever platform is locally present. Importing
// "all platforms" (containerd's default) walks the whole index and fails
// with "content digest ...: not found" the moment it reaches a
// declared-but-not-included foreign-platform manifest; every archive
// LoadImages ever receives (both docker-image's live "docker save" and
// image-archive's on-disk tar, plus internal/imagebundle's own crane.Save
// output) was produced on this same host/arch, so the only platform that
// can ever actually be present is the host's own.
var importPlatform = platforms.DefaultStrict()

// containerdImportTimeout bounds importCachedImages's wait for the
// cluster's containerd socket to accept connections. Matches
// internal/bootstrap's own containerdReadyTimeout: if containerd never
// comes up at all, the concurrently-running bootstrap.Boot call already
// fails within that same bound, so this adds no serial wait of its own.
const containerdImportTimeout = 30 * time.Second

// socketPollInterval governs waitContainerdSocket's readiness poll,
// matching internal/bootstrap's own poll cadence (see its pollInterval).
const socketPollInterval = 20 * time.Millisecond

// containerdSocketPath is the socket internal/bootstrap/config.go's
// writeContainerdConfig configures the cluster's own containerd instance to
// listen on. hostproc has no VM boundary (see package doc), so this socket
// is directly reachable from the host with no forwarding — unlike vz's
// guest-only equivalent, which needs a control-agent round trip instead
// (see vz's LoadImages doc comment).
func (r *Runtime) containerdSocketPath(name string) string {
	return filepath.Join(r.dataDir(name), "containerd", "containerd.sock")
}

// LoadImages imports each of images into the cluster's containerd content
// and image store, in the "k8s.io" namespace CRI (and so kubelet) reads
// from — the same namespace kind's per-node "ctr -n k8s.io images import"
// targets, but via containerd's own Go client against the cluster's socket
// directly rather than shelling out to the ctr binary.
//
// Each image is imported from its own archive stream, one at a time: kind's
// "load docker-image" instead builds a single multi-image tarball and then
// resends that whole tarball once per requested image (kind#3063); reading
// (and, for the docker-image loader, exporting) each image's own archive
// exactly once avoids that
// resend-the-world behavior. Layers shared across images (a common base
// image, or a repeat load of an image already present) are deduplicated for
// free by containerd's content-addressed store: Import only writes blobs
// whose digest isn't already there.
func (r *Runtime) LoadImages(ctx context.Context, name string, images []substrate.ImageSource) error {
	if _, err := os.Stat(r.runningMarkerPath(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hostproc: cluster %q is not running", name)
		}

		return fmt.Errorf("hostproc: checking cluster %q: %w", name, err)
	}

	client, err := containerd.New(r.containerdSocketPath(name), containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return fmt.Errorf("hostproc: connecting to cluster %q's containerd: %w", name, err)
	}
	defer func() { _ = client.Close() }()

	return importImages(ctx, client, images)
}

// importImages is LoadImages's and importCachedImages's shared core: it
// imports each of images into client's image store, one at a time (see
// LoadImages's doc comment for why one-at-a-time, not a single combined
// tarball).
func importImages(ctx context.Context, client *containerd.Client, images []substrate.ImageSource) error {
	for _, img := range images {
		if _, err := client.Import(ctx, img.Stream, containerd.WithImportPlatform(importPlatform)); err != nil {
			return fmt.Errorf("hostproc: importing %s: %w", img.Reference, err)
		}
	}

	return nil
}

// waitContainerdSocket polls until socketPath accepts a connection and
// returns a connected containerd client in the "k8s.io" namespace, or ctx
// is done. Used by importCachedImages, which — unlike LoadImages, which
// only ever runs against an already-running cluster — is started
// concurrently with the same Start call that is still bringing containerd
// up (see hostproc.go's Start), so it cannot assume the socket already
// exists.
func waitContainerdSocket(ctx context.Context, socketPath string) (*containerd.Client, error) {
	var lastErr error

	for {
		if conn, dialErr := net.Dial("unix", socketPath); dialErr == nil {
			_ = conn.Close()

			client, err := containerd.New(socketPath, containerd.WithDefaultNamespace("k8s.io"))
			if err == nil {
				return client, nil
			}

			lastErr = err
		} else {
			lastErr = dialErr
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for containerd socket %s: %w (last error: %v)", socketPath, ctx.Err(), lastErr)
		case <-time.After(socketPollInterval):
		}
	}
}
