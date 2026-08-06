package components

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// buildTestTarGzWithSymlink builds a gzip-compressed tar archive containing
// one regular file and one symlink pointing at it, mirroring the shape
// Alpine's apk packages use (e.g. usr/sbin/iptables -> xtables-nft-multi).
func buildTestTarGzWithSymlink(t *testing.T, regularName, content, symlinkName, linkTarget string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{Name: regularName, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("WriteHeader(%s): %v", regularName, err)
	}

	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("Write(%s): %v", regularName, err)
	}

	if err := tw.WriteHeader(&tar.Header{Name: symlinkName, Typeflag: tar.TypeSymlink, Linkname: linkTarget, Mode: 0o777}); err != nil {
		t.Fatalf("WriteHeader(%s): %v", symlinkName, err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	return buf.Bytes()
}

func TestExtractTarGzPreserveSymlinks_RecreatesSymlink(t *testing.T) {
	t.Parallel()

	tarGz := buildTestTarGzWithSymlink(t, "usr/sbin/xtables-nft-multi", "binary content", "usr/sbin/iptables", "xtables-nft-multi")

	destDir := t.TempDir()
	if err := extractTarGzPreserveSymlinks(tarGz, destDir); err != nil {
		t.Fatalf("extractTarGzPreserveSymlinks: %v", err)
	}

	link := filepath.Join(destDir, "usr", "sbin", "iptables")

	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink(%s): %v", link, err)
	}

	if target != "xtables-nft-multi" {
		t.Errorf("symlink target = %q, want %q", target, "xtables-nft-multi")
	}

	got, err := os.ReadFile(link) // follows the symlink
	if err != nil {
		t.Fatalf("reading through symlink: %v", err)
	}

	if string(got) != "binary content" {
		t.Errorf("content through symlink = %q, want %q", got, "binary content")
	}
}

func TestExtractTarGzPreserveSymlinks_AllowsContainedParentDirTarget(t *testing.T) {
	t.Parallel()

	// Mirrors gcompat's real layout: lib64/ld-linux-aarch64.so.1 ->
	// ../lib/ld-linux-aarch64.so.1 — a "../" component that stays inside
	// destDir must be allowed, not rejected as if it were an escape
	// attempt (found live: the original implementation rejected any
	// symlink target containing "/" at all).
	tarGz := buildTestTarGzWithSymlink(t, "lib/ld-linux-aarch64.so.1", "loader content", "lib64/ld-linux-aarch64.so.1", "../lib/ld-linux-aarch64.so.1")

	destDir := t.TempDir()
	if err := extractTarGzPreserveSymlinks(tarGz, destDir); err != nil {
		t.Fatalf("extractTarGzPreserveSymlinks: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "lib64", "ld-linux-aarch64.so.1"))
	if err != nil {
		t.Fatalf("reading through symlink: %v", err)
	}

	if string(got) != "loader content" {
		t.Errorf("content through symlink = %q, want %q", got, "loader content")
	}
}

func TestExtractTarGzPreserveSymlinks_RejectsEscapingSymlinkTarget(t *testing.T) {
	t.Parallel()

	tarGz := buildTestTarGzWithSymlink(t, "usr/sbin/xtables-nft-multi", "binary content", "usr/sbin/evil", "../../../etc/passwd")

	if err := extractTarGzPreserveSymlinks(tarGz, t.TempDir()); err == nil {
		t.Fatal("extractTarGzPreserveSymlinks with an escaping symlink target = nil error, want error")
	}
}

func TestExtractTarGzPreserveSymlinks_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	content := []byte("evil")
	if err := tw.WriteHeader(&tar.Header{Name: "../../etc/passwd", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}

	if _, err := tw.Write(content); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	if err := extractTarGzPreserveSymlinks(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("extractTarGzPreserveSymlinks with a path-traversal entry = nil error, want error")
	}
}

func TestEnsureIPTablesBundle_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	dir := filepath.Join(c.dir, "iptables-"+IPTablesBundleKey)
	if err := os.MkdirAll(filepath.Join(dir, "usr", "sbin"), 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "usr", "sbin", "xtables-nft-multi"), []byte("prebuilt"), 0o755); err != nil {
		t.Fatalf("seeding marker file: %v", err)
	}

	// c.client has no reachable server for this test; a cache miss would
	// fail on the real network call, so success proves the cache-hit
	// path was taken.
	bundle, err := c.EnsureIPTablesBundle(context.Background())
	if err != nil {
		t.Fatalf("EnsureIPTablesBundle (cache hit): %v", err)
	}

	if bundle.Dir != dir {
		t.Errorf("Dir = %s, want %s", bundle.Dir, dir)
	}
}
