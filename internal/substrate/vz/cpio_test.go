//go:build darwin

package vz

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCpioWriter_WriteFileThenReadBackViaCpioBinary(t *testing.T) {
	t.Parallel()

	w := newCpioWriter()

	if err := w.WriteFile("hello.txt", 0o644, []byte("hello cpio")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := w.WriteDir("adir", 0o755); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}

	if err := w.WriteFile("adir/nested.txt", 0o755, []byte("nested content")); err != nil {
		t.Fatalf("WriteFile (nested): %v", err)
	}

	if err := w.WriteSymlink("alink", "hello.txt"); err != nil {
		t.Fatalf("WriteSymlink: %v", err)
	}

	data, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if len(data)%512 != 0 {
		t.Errorf("archive length %d is not a multiple of 512", len(data))
	}

	extractWithSystemCpio(t, data)
}

// extractWithSystemCpio shells out to the system `cpio` binary (present on
// macOS by default) to extract the archive this package's writer produced
// — an independent implementation validating newc format compliance rather
// than testing the writer against its own encoding logic.
func extractWithSystemCpio(t *testing.T, data []byte) {
	t.Helper()

	if _, err := os.Stat("/usr/bin/cpio"); err != nil {
		t.Skip("no /usr/bin/cpio on this host to cross-validate against")
	}

	extractDir := extractArchiveForTest(t, data)

	got, err := os.ReadFile(filepath.Join(extractDir, "hello.txt"))
	if err != nil {
		t.Fatalf("reading extracted hello.txt: %v", err)
	}

	if string(got) != "hello cpio" {
		t.Errorf("hello.txt content = %q, want %q", got, "hello cpio")
	}

	nested, err := os.ReadFile(filepath.Join(extractDir, "adir", "nested.txt"))
	if err != nil {
		t.Fatalf("reading extracted adir/nested.txt: %v", err)
	}

	if string(nested) != "nested content" {
		t.Errorf("adir/nested.txt content = %q, want %q", nested, "nested content")
	}

	link, err := os.Readlink(filepath.Join(extractDir, "alink"))
	if err != nil {
		t.Fatalf("reading extracted symlink: %v", err)
	}

	if link != "hello.txt" {
		t.Errorf("alink target = %q, want %q", link, "hello.txt")
	}
}

func TestCpioWriter_RejectsAbsolutePaths(t *testing.T) {
	t.Parallel()

	w := newCpioWriter()
	if err := w.WriteFile("/etc/passwd", 0o644, []byte("x")); err == nil {
		t.Fatal("WriteFile with an absolute path = nil error, want error")
	}
}

func TestCpioWriter_HeaderIsWellFormedNewcMagic(t *testing.T) {
	t.Parallel()

	w := newCpioWriter()
	if err := w.WriteFile("f", 0o644, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if !bytes.HasPrefix(data, []byte("070701")) {
		t.Errorf("archive does not start with newc magic \"070701\": %q", data[:6])
	}

	if !strings.Contains(string(data), "TRAILER!!!") {
		t.Error("archive missing TRAILER!!! entry")
	}
}
