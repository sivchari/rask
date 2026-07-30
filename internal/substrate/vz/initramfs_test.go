//go:build darwin

package vz

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
