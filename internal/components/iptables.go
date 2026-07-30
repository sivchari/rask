package components

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// iptablesPackage is one Alpine aarch64 apk this bundle downloads, pinned by
// exact version and sha256 (see kernel.go's guestKernelSHA256 doc comment
// for why a plain sha256 of the downloaded file, not Alpine's own APKINDEX
// "C:" field).
type iptablesPackage struct {
	name, version, sha256 string
}

// iptablesPackages are every Alpine package rask-init needs to run
// kube-proxy's iptables mode inside the vz guest: kube-proxy execs the real
// iptables-save/iptables-restore binaries (see k8s.io/kubernetes's
// pkg/util/iptables), which the guest's otherwise-static-Go/no-libc
// initramfs does not otherwise provide. iptables is Alpine's nft-backed
// busybox-style multi-call binary (xtables-nft-multi, with
// iptables/iptables-save/iptables-restore/ip6tables/... as symlinks to it),
// dynamically linked against musl libc plus libmnl/libnftnl/libxtables.
var iptablesPackages = []iptablesPackage{
	{name: "musl", version: "1.2.5-r11", sha256: "721010e6bff908878d9c527428598661be59dde0d9f013f8431d01fd4dd16652"},
	{name: "libmnl", version: "1.0.5-r2", sha256: "a746622c068d36803f62a208b9cd4480565b55a5e1290d475599f94822ae0c24"},
	{name: "libnftnl", version: "1.2.8-r0", sha256: "2e18aa79b5581f61522d03b8e2d551ad51a0e240c6f9f33f76655da04a3a6785"},
	{name: "libxtables", version: "1.8.11-r1", sha256: "7e414a9b68be53dbecacfa7d3adcf8340bb07b299dc595378adb5cadc9a818e8"},
	{name: "iptables", version: "1.8.11-r1", sha256: "09f11de18f75653ccae83190f20b2dffb48533186d720ae23b73904be90ff89e"},
}

// IPTablesBundleVersion identifies this pinned package set for cache
// directory naming.
const IPTablesBundleVersion = "1.8.11-r1"

func (p iptablesPackage) url() string {
	return fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/%s/main/aarch64/%s-%s.apk", AlpineVersion, p.name, p.version)
}

// IPTablesBundle is the resolved, merged file tree of every binary/shared
// library rask-init needs to run kube-proxy's iptables mode: extracting it
// directly into the guest root at the same relative paths the packages
// themselves use (lib/, usr/lib/, usr/sbin/) reproduces Alpine's own layout,
// including the symlinks (e.g. usr/sbin/iptables -> xtables-nft-multi) the
// real packages ship, so no synthetic relinking is needed.
type IPTablesBundle struct {
	Dir string
}

// EnsureIPTablesBundle downloads (if not already cached), verifies and
// extracts every Alpine package in iptablesPackages into one merged
// directory tree, preserving their internal symlinks.
func (c *Cache) EnsureIPTablesBundle(ctx context.Context) (*IPTablesBundle, error) {
	dir := filepath.Join(c.dir, "iptables-"+IPTablesBundleVersion)
	marker := filepath.Join(dir, "usr", "sbin", "xtables-nft-multi")

	if _, err := os.Stat(marker); err == nil {
		return &IPTablesBundle{Dir: dir}, nil
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	for _, pkg := range iptablesPackages {
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

	return &IPTablesBundle{Dir: dir}, nil
}

// extractTarGzPreserveSymlinks extracts a gzip-compressed tar archive into
// destDir like extractTarGz, but additionally recreates symlink entries
// (extractTarGz silently skips them: none of rask's other pinned releases
// use symlinks, but Alpine's apk packages do — e.g. usr/sbin/iptables ->
// xtables-nft-multi). Entries, including symlink targets, that would
// escape destDir are rejected the same way extractTarGz's safeJoin does.
func extractTarGzPreserveSymlinks(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := extractFile(tr, target, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := extractSymlink(destDir, hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Not present in this bundle's pinned packages (device
			// nodes, hardlinks, etc.); skip rather than fail.
		}
	}
}

// extractSymlink recreates a symlink at target pointing at linkname
// (resolved relative to target's own directory, matching normal symlink
// semantics — e.g. gcompat's lib64/ld-linux-aarch64.so.1 legitimately
// points at "../lib/ld-linux-aarch64.so.1"), rejecting any linkname whose
// resolved target would land outside destDir.
func extractSymlink(destDir, linkname, target string) error {
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("components: rejecting symlink %s -> %q: absolute target", target, linkname)
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), linkname))

	rel, err := filepath.Rel(destDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("components: rejecting symlink %s -> %q: escapes destination directory", target, linkname)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("removing existing %s: %w", target, err)
	}

	if err := os.Symlink(linkname, target); err != nil {
		return fmt.Errorf("creating symlink %s -> %s: %w", target, linkname, err)
	}

	return nil
}
