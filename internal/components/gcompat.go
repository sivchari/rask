package components

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// gcompatPackages are every Alpine aarch64 package rask-init needs to run
// glibc-dynamically-linked binaries on top of the guest's otherwise
// musl-free, all-static initramfs.
//
// containerd and ctr (internal/components' pinned upstream releases) and
// kubelet (dl.k8s.io's release) are dynamically linked against glibc
// (confirmed live: `file` reports "interpreter /lib/ld-linux-aarch64.so.1"
// for all three, unlike kube-apiserver/kube-controller-manager/kube-
// scheduler/kube-proxy/kubectl, kine and containerd-shim-runc-v2, which are
// static) — exec fails with a misleading "no such file or directory" (the
// real cause is the missing ELF interpreter, not the binary itself) without
// this. gcompat is Alpine's own supported answer to "run a glibc binary on
// a musl system": it ships /lib/ld-linux-aarch64.so.1 plus libc.so.6 (and
// every other glibc SONAME containerd/kubelet reference — libpthread.so.0,
// libresolv.so.2 — confirmed via `strings <binary> | grep '\.so'`) as
// symlinks to a single translation-shim library, rather than a full glibc
// build.
var gcompatPackages = []iptablesPackage{
	{name: "musl", version: "1.2.5-r11", sha256: "721010e6bff908878d9c527428598661be59dde0d9f013f8431d01fd4dd16652"},
	{name: "libucontext", version: "1.3.2-r0", sha256: "55e63604cb0d3819e94b122bdec6277e8ad1900554ee1f84a69eaa0b0733f94c"},
	{name: "musl-obstack", version: "1.2.3-r2", sha256: "7501708485edf27627745c40747e404a3166e702bd32f485ded5d17df3f1d4a9"},
	{name: "gcompat", version: "1.1.0-r4", sha256: "9051217a6d513c9a29c4d183244eb864b108a3daf9e14ec51a6d6712d009ce4e"},
}

// GCompatBundleKey is a content-addressed identifier for gcompatPackages
// (see packageSetKey), used both for this bundle's own cache directory
// name and as an input to internal/substrate/vz's template initramfs
// cache key.
var GCompatBundleKey = packageSetKey(gcompatPackages)

// GCompatBundle is the resolved, merged file tree containing
// /lib/ld-linux-aarch64.so.1 and every glibc-compat shared library
// dynamically-linked guest binaries need, extracted at the same relative
// paths (lib/, lib64/, usr/lib/) the packages themselves use.
type GCompatBundle struct {
	Dir string
}

// EnsureGCompatBundle downloads (if not already cached), verifies and
// extracts every package in gcompatPackages into one merged directory
// tree, preserving their internal symlinks (e.g. lib/libc.so.6 ->
// libgcompat.so.0).
func (c *Cache) EnsureGCompatBundle(ctx context.Context) (*GCompatBundle, error) {
	dir := filepath.Join(c.dir, "gcompat-"+GCompatBundleKey)
	marker := filepath.Join(dir, "lib", "ld-linux-aarch64.so.1")

	if _, err := os.Stat(marker); err == nil {
		return &GCompatBundle{Dir: dir}, nil
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	for _, pkg := range gcompatPackages {
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

	return &GCompatBundle{Dir: dir}, nil
}
