package components

import (
	"context"
	"fmt"
	"path/filepath"
)

// raskInitArchiveFilename, raskInitURL and raskInitChecksumURL build the
// download and checksum URLs for a rask-init release asset. rask-init
// (cmd/rask-init, the vz substrate's guest PID 1) is published alongside
// the main rask release (see .goreleaser.yml's "rask-init" build/archive)
// specifically so EnsureRaskInit has something to download: the binary
// internal/substrate/vz/embedded go:embeds is committed to git only as a
// placeholder, real only when `make build-rask-init` cross-compiled it
// fresh into a working tree — never true for a Go module consumer's
// read-only module cache.
//
// version has no leading "v" (e.g. "0.1.5"), matching goreleaser's
// {{ .Version }} and rask.Version; the release tag itself (used in the
// URL path) does.
func raskInitArchiveFilename(version string) string {
	return fmt.Sprintf("rask-init_%s_linux_arm64.tar.gz", version)
}

func raskInitURL(version string) string {
	return fmt.Sprintf("https://github.com/sivchari/rask/releases/download/v%s/%s", version, raskInitArchiveFilename(version))
}

func raskInitChecksumURL(version string) string {
	return fmt.Sprintf("https://github.com/sivchari/rask/releases/download/v%s/rask_%s_checksums.txt", version, version)
}

// EnsureRaskInit downloads (if not already cached), verifies against that
// release's published checksums file, and extracts the rask-init binary
// for the exact tagged rask release "version". Idempotent per version: a
// second call for the same version does no network I/O, so downgrading
// back to an already-fetched version never re-downloads it.
func (c *Cache) EnsureRaskInit(ctx context.Context, version string) (string, error) {
	dir, err := c.ensureArchive(ctx, filepath.Join("rask-init", version), raskInitArchiveFilename(version), raskInitURL(version), raskInitChecksumURL(version), raskInitArchiveFilename(version))
	if err != nil {
		return "", fmt.Errorf("components: fetching rask-init %s: %w", version, err)
	}

	return filepath.Join(dir, "rask-init"), nil
}
