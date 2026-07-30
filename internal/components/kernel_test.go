package components

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildZimgFixture builds a minimal EFI-ZBOOT-shaped file wrapping a
// gzip-compressed payload, matching the layout extractZimgPayload parses:
// "MZ\x00\x00" + "zimg" + LE32 offset + LE32 size + 8 reserved bytes, then
// the gzip payload starting at that offset.
func buildZimgFixture(t *testing.T, payload []byte) []byte {
	t.Helper()

	var gz bytes.Buffer

	w := gzip.NewWriter(&gz)
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	const headerSize = zimgHeaderSize

	buf := make([]byte, headerSize)
	copy(buf[0:4], "MZ\x00\x00")
	copy(buf[4:8], "zimg")
	binary.LittleEndian.PutUint32(buf[8:12], uint32(headerSize))
	binary.LittleEndian.PutUint32(buf[12:16], uint32(gz.Len()))

	return append(buf, gz.Bytes()...)
}

func TestExtractZimgPayload_ValidHeader(t *testing.T) {
	t.Parallel()

	want := []byte("fake arm64 kernel Image contents")
	path := filepath.Join(t.TempDir(), "vmlinuz-virt")

	if err := os.WriteFile(path, buildZimgFixture(t, want), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got, err := extractZimgPayload(path)
	if err != nil {
		t.Fatalf("extractZimgPayload: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("extractZimgPayload = %q, want %q", got, want)
	}
}

func TestExtractZimgPayload_RejectsMissingMagic(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-zimg")
	if err := os.WriteFile(path, []byte("MZ\x00\x00garbage-not-zimg-here-------"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := extractZimgPayload(path); err == nil {
		t.Fatal("extractZimgPayload with missing zimg magic = nil error, want error")
	}
}

func TestExtractZimgPayload_RejectsPayloadPastEOF(t *testing.T) {
	t.Parallel()

	buf := make([]byte, zimgHeaderSize)
	copy(buf[0:4], "MZ\x00\x00")
	copy(buf[4:8], "zimg")
	binary.LittleEndian.PutUint32(buf[8:12], uint32(zimgHeaderSize))
	binary.LittleEndian.PutUint32(buf[12:16], 1<<20) // claims a 1MiB payload the file doesn't have

	path := filepath.Join(t.TempDir(), "truncated")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := extractZimgPayload(path); err == nil {
		t.Fatal("extractZimgPayload with out-of-range payload = nil error, want error")
	}
}

func TestExtractZimgPayload_RejectsCorruptGzip(t *testing.T) {
	t.Parallel()

	buf := make([]byte, zimgHeaderSize)
	copy(buf[0:4], "MZ\x00\x00")
	copy(buf[4:8], "zimg")
	binary.LittleEndian.PutUint32(buf[8:12], uint32(zimgHeaderSize))
	binary.LittleEndian.PutUint32(buf[12:16], 4)
	buf = append(buf, []byte("nope")...)

	path := filepath.Join(t.TempDir(), "badgzip")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := extractZimgPayload(path); err == nil {
		t.Fatal("extractZimgPayload with corrupt gzip payload = nil error, want error")
	}
}

func TestEnsureGuestKernel_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	dir := filepath.Join(c.dir, "guest-kernel-"+GuestKernelPkgVersion)
	if err := os.MkdirAll(filepath.Join(dir, "lib", "modules", GuestKernelRelease), 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Image"), []byte("prebuilt image"), 0o644); err != nil {
		t.Fatalf("seeding Image: %v", err)
	}

	// c.client is http.DefaultClient with no server behind it reachable
	// from this test; a cache miss would hang/fail on a real network
	// call, so success here proves the cache-hit path took priority.
	kernel, err := c.EnsureGuestKernel(context.Background())
	if err != nil {
		t.Fatalf("EnsureGuestKernel (cache hit): %v", err)
	}

	if kernel.ImagePath != filepath.Join(dir, "Image") {
		t.Errorf("ImagePath = %s, want %s", kernel.ImagePath, filepath.Join(dir, "Image"))
	}

	if kernel.Release != GuestKernelRelease {
		t.Errorf("Release = %s, want %s", kernel.Release, GuestKernelRelease)
	}
}
