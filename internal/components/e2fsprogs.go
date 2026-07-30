package components

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// e2fsprogsPackages are every Alpine aarch64 package rask-init needs to run
// mkfs.ext4 once, at first boot, to format the per-cluster virtio-blk data
// disk (see plan-m0-spikes.md's guest layout: /var lives on this disk, not
// tmpfs). mkfs.ext4 (part of e2fsprogs) is dynamically linked against musl
// plus e2fsprogs-libs/libblkid/libcom_err/libuuid/libeconf; this is the full
// transitive closure (resolved against Alpine's v3.21 APKINDEX "p:"/"D:"
// fields, the same way iptablesPackages was).
var e2fsprogsPackages = []iptablesPackage{
	{name: "musl", version: "1.2.5-r11", sha256: "721010e6bff908878d9c527428598661be59dde0d9f013f8431d01fd4dd16652"},
	{name: "libeconf", version: "0.6.3-r0", sha256: "db25f8abaf1ec61f5dcc0ff9b55271a3f6e43037e430b0c126ac000483e17dad"},
	{name: "libuuid", version: "2.40.4-r1", sha256: "d392ac96027cbe5ca9bc270d2aa4e99747b0eae3cf65a0087f2c6f13a7382702"},
	{name: "libcom_err", version: "1.47.1-r1", sha256: "0798fedbc002cada8e74a7cb4cdae885e5f0c1b5fd59b15af5b2903b95d30153"},
	{name: "libblkid", version: "2.40.4-r1", sha256: "61339f8737b05062f662cfc30c65cb34f10ccc53573846653551c66156c4aa0b"},
	{name: "e2fsprogs-libs", version: "1.47.1-r1", sha256: "c817ddefbfa19245cce0c62820a1eb50c771f4999232d0a1c9e57521a58508d3"},
	{name: "e2fsprogs", version: "1.47.1-r1", sha256: "c28dddb51a40a91820a9f0dcd32f19abf23c8256543d7afc4f87363b28885a66"},
}

// E2fsprogsBundleVersion identifies this pinned package set for cache
// directory naming.
const E2fsprogsBundleVersion = "1.47.1-r1"

// E2fsprogsBundle is the resolved, merged file tree containing mkfs.ext4
// (sbin/mkfs.ext4) and every shared library it needs, extracted at the same
// relative paths (lib/, usr/lib/, sbin/) the packages themselves use.
type E2fsprogsBundle struct {
	Dir string
}

// EnsureE2fsprogsBundle downloads (if not already cached), verifies and
// extracts every package in e2fsprogsPackages into one merged directory
// tree, preserving their internal symlinks (e.g. sbin/mkfs.ext4 ->
// mke2fs).
func (c *Cache) EnsureE2fsprogsBundle(ctx context.Context) (*E2fsprogsBundle, error) {
	dir := filepath.Join(c.dir, "e2fsprogs-"+E2fsprogsBundleVersion)
	marker := filepath.Join(dir, "sbin", "mke2fs")

	if _, err := os.Stat(marker); err == nil {
		return &E2fsprogsBundle{Dir: dir}, nil
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	for _, pkg := range e2fsprogsPackages {
		data, err := c.fetch(ctx, pkg.url())
		if err != nil {
			_ = os.RemoveAll(tmpDir)

			return nil, fmt.Errorf("components: downloading %s: %w", pkg.name, err)
		}

		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != pkg.sha256 {
			_ = os.RemoveAll(tmpDir)

			return nil, fmt.Errorf("components: %s sha256 mismatch: got %s, want %s", pkg.name, got, pkg.sha256)
		}

		if err := extractTarGzPreserveSymlinks(data, tmpDir); err != nil {
			_ = os.RemoveAll(tmpDir)

			return nil, fmt.Errorf("components: extracting %s: %w", pkg.name, err)
		}
	}

	if err := os.Rename(tmpDir, dir); err != nil {
		return nil, fmt.Errorf("components: finalizing %s: %w", dir, err)
	}

	return &E2fsprogsBundle{Dir: dir}, nil
}
