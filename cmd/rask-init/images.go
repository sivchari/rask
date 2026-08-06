//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/platforms"

	"github.com/sivchari/rask/internal/guestlayout"
)

// containerdImportTimeout bounds importPrefetchedImages' wait for this
// guest's containerd socket to accept connections, matching
// internal/bootstrap's own containerdReadyTimeout: if containerd never
// comes up at all, the concurrently-running bootstrap.Boot call already
// fails within that same bound, so this adds no serial wait of its own.
const containerdImportTimeout = 30 * time.Second

// socketPollInterval governs waitContainerdSocket's readiness poll,
// matching internal/substrate/hostproc's own poll cadence.
const socketPollInterval = 20 * time.Millisecond

// importPlatform restricts importPrefetchedImages to importing only this
// guest's own platform out of an archive's manifest — every archive
// internal/imagebundle's own crane.Save produced was built for arm64 (the
// only architecture a vz guest ever runs), matching
// internal/substrate/hostproc's identical importPlatform for the same
// reason (see that package's loadimages.go doc comment).
var importPlatform = platforms.DefaultStrict()

// importPrefetchedImages imports every docker-save style tar archive
// internal/substrate/vz's images overlay cpio placed under
// guestlayout.ImagesDir into this guest's own containerd, so kubelet's
// later CoreDNS/local-path-provisioner pulls (and every pod sandbox's
// pause image) find the image already present instead of going to the
// network — mirroring internal/substrate/hostproc.Runtime's
// importCachedImages, adapted for a guest with no host-reachable
// containerd socket of its own to dial from outside (see vz's LoadImages
// doc comment for that gap; this runs entirely guest-side instead, so it
// needs no such channel).
//
// Best-effort only, exactly like hostproc's importCachedImages: a missing
// ImagesDir (no images were prefetched host-side, or none were reachable
// when internal/substrate/vz's Runtime.Create tried) or any import failure
// is logged to the console, never fatal — a pod with imagePullPolicy:
// IfNotPresent falls back to a live registry pull exactly as it did before
// this existed.
func importPrefetchedImages(ctx context.Context, containerdSocketPath string) {
	entries, err := os.ReadDir(guestlayout.ImagesDir)
	if err != nil {
		return // nothing staged — the common case of no cached images.
	}

	if len(entries) == 0 {
		return
	}

	importCtx, cancel := context.WithTimeout(ctx, containerdImportTimeout)
	defer cancel()

	client, err := waitContainerdSocket(importCtx, containerdSocketPath)
	if err != nil {
		fmt.Printf("RASK-INIT-IMAGE-IMPORT-FAILED waiting for containerd: %v\n", err)

		return
	}
	defer func() { _ = client.Close() }()

	imported := 0

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		path := filepath.Join(guestlayout.ImagesDir, e.Name())

		if err := importImageArchive(importCtx, client, path); err != nil {
			fmt.Printf("RASK-INIT-IMAGE-IMPORT-FAILED importing %s: %v\n", e.Name(), err)

			continue
		}

		imported++
	}

	fmt.Printf("RASK-INIT-IMAGES-IMPORTED count=%d\n", imported)
}

// importImageArchive imports a single docker-save style tar archive at
// path into client's image store.
func importImageArchive(ctx context.Context, client *containerd.Client, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := client.Import(ctx, f, containerd.WithImportPlatform(importPlatform)); err != nil {
		return fmt.Errorf("importing %s: %w", path, err)
	}

	return nil
}

// waitContainerdSocket polls until socketPath accepts a connection and
// returns a connected containerd client in the "k8s.io" namespace (the
// same namespace CRI, and so kubelet, reads from), or ctx is done. Used
// because importPrefetchedImages is started concurrently with the same
// runBoot call that is still bringing containerd up (see boot.go's
// runBoot), so it cannot assume the socket already exists — mirrors
// internal/substrate/hostproc's identical waitContainerdSocket.
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
