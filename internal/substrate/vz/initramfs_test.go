//go:build darwin

package vz

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// TestBuildTemplateInitramfs_InitEntryIsExecutableAndMatchesResolved guards
// against the regression class this package's own investigation targeted:
// buildTemplateInitramfs writing /init with the wrong mode, or with content
// that doesn't match what embedded.Resolve actually returned, either of
// which wedges the guest at boot (the kernel can't exec a non-executable or
// truncated/mismatched PID 1) with no error surfaced on the host side until
// a boot timeout fires minutes later. Exercises buildTemplateInitramfs's
// exact `w.WriteFile("init", 0o755, raskInitBinary)` statement against
// embedded.Resolve's real output (not a fixture), so a regression in either
// Resolve's fallback chain or the cpio encoding itself fails this test
// without ever booting a VM.
func TestBuildTemplateInitramfs_InitEntryIsExecutableAndMatchesResolved(t *testing.T) {
	if embedded.IsPlaceholder() {
		t.Skip("embedded/rask-init is still the placeholder; run `make build-rask-init` to cross-compile the real binary")
	}

	raskInitBinary, err := embedded.Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("embedded.Resolve: %v", err)
	}

	w := newCpioWriter()
	if err := w.WriteFile("init", 0o755, raskInitBinary); err != nil {
		t.Fatalf("WriteFile(init): %v", err)
	}

	data, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	extractDir := extractArchiveForTest(t, data)
	initPath := filepath.Join(extractDir, "init")

	info, err := os.Stat(initPath)
	if err != nil {
		t.Fatalf("stat extracted init: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("extracted /init mode = %o, want %o", perm, 0o755)
	}

	if info.Mode()&0o111 == 0 {
		t.Error("extracted /init has no execute bit set")
	}

	got, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("reading extracted init: %v", err)
	}

	if !bytes.Equal(got, raskInitBinary) {
		t.Errorf("extracted /init content does not match embedded.Resolve's output (got %d bytes, want %d bytes)", len(got), len(raskInitBinary))
	}

	if len(got) < 4 || got[0] != 0x7f || got[1] != 'E' || got[2] != 'L' || got[3] != 'F' {
		t.Error("extracted /init does not start with the ELF magic number")
	}
}

func TestCopyLocalTree_PreservesFilesDirsAndSymlinks(t *testing.T) {
	t.Parallel()

	src := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(src, "sub", "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := os.Symlink("file.txt", filepath.Join(src, "sub", "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	w := newCpioWriter()
	if err := copyLocalTree(w, "opt/rask/bin", src); err != nil {
		t.Fatalf("copyLocalTree: %v", err)
	}

	data, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	extracted := extractArchiveForTest(t, data)

	got, err := os.ReadFile(filepath.Join(extracted, "opt", "rask", "bin", "sub", "file.txt"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}

	if string(got) != "content" {
		t.Errorf("copied file content = %q, want %q", got, "content")
	}

	link, err := os.Readlink(filepath.Join(extracted, "opt", "rask", "bin", "sub", "link.txt"))
	if err != nil {
		t.Fatalf("reading copied symlink: %v", err)
	}

	if link != "file.txt" {
		t.Errorf("copied symlink target = %q, want %q", link, "file.txt")
	}
}

func TestCopyLocalTree_DuplicateFileIsIdempotent(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "shared.so"), []byte("shared content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	w := newCpioWriter()

	if err := copyLocalTree(w, "lib", src); err != nil {
		t.Fatalf("copyLocalTree (1st): %v", err)
	}

	// Simulates two bundles (e.g. iptables + e2fsprogs) both shipping the
	// same musl shared library at the same guest path: must not error or
	// produce a duplicate cpio entry.
	if err := copyLocalTree(w, "lib", src); err != nil {
		t.Fatalf("copyLocalTree (2nd, duplicate): %v", err)
	}

	data, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	extracted := extractArchiveForTest(t, data)

	got, err := os.ReadFile(filepath.Join(extracted, "lib", "shared.so"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}

	if string(got) != "shared content" {
		t.Errorf("copied file content = %q, want %q", got, "shared content")
	}
}

// extractArchiveForTest extracts a cpio archive with the system cpio binary
// into a fresh temp directory and returns its path.
func extractArchiveForTest(t *testing.T, data []byte) string {
	t.Helper()

	if _, err := os.Stat("/usr/bin/cpio"); err != nil {
		t.Skip("no /usr/bin/cpio on this host to extract with")
	}

	dir := t.TempDir()

	f, err := os.CreateTemp(dir, "archive-*.cpio")
	if err != nil {
		t.Fatalf("creating temp archive file: %v", err)
	}

	if _, err := f.Write(data); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("closing archive: %v", err)
	}

	if _, err := f.Seek(0, 0); err == nil {
		_ = f.Close()
	}

	extractDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	archive, err := os.Open(f.Name())
	if err != nil {
		t.Fatalf("reopening archive: %v", err)
	}
	defer func() { _ = archive.Close() }()

	cmd := exec.Command("/usr/bin/cpio", "-i", "-d")
	cmd.Dir = extractDir
	cmd.Stdin = archive

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cpio -i: %v: %s", err, out)
	}

	return extractDir
}
