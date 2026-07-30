package components

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCache_Dir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := NewCache(dir)

	if got := c.Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

func TestEnsureFile_DownloadsVerifiesAndCaches(t *testing.T) {
	t.Parallel()

	content := []byte("#!/bin/sh\necho hi\n")
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])

	downloads := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin/mytool":
			downloads++

			_, _ = w.Write(content)
		case "/bin/mytool.sha256":
			_, _ = w.Write([]byte(checksum))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCache(t.TempDir())
	ctx := context.Background()

	path, err := c.ensureFile(ctx, "mytool-v1-arm64", "mytool", srv.URL+"/bin/mytool", srv.URL+"/bin/mytool.sha256", "mytool")
	if err != nil {
		t.Fatalf("ensureFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cached file: %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Errorf("cached content = %q, want %q", got, content)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("cached file mode = %v, want executable", info.Mode())
	}

	// A second call must be a cache hit: no further download.
	if _, err := c.ensureFile(ctx, "mytool-v1-arm64", "mytool", srv.URL+"/bin/mytool", srv.URL+"/bin/mytool.sha256", "mytool"); err != nil {
		t.Fatalf("ensureFile (cached): %v", err)
	}

	if downloads != 1 {
		t.Errorf("downloads = %d, want 1 (second call should hit the cache)", downloads)
	}
}

func TestEnsureFile_ChecksumMismatchFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin/mytool":
			_, _ = w.Write([]byte("actual content"))
		case "/bin/mytool.sha256":
			_, _ = w.Write([]byte(hex.EncodeToString(sha256Sum([]byte("different content")))))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCache(t.TempDir())
	ctx := context.Background()

	if _, err := c.ensureFile(ctx, "mytool-v1-arm64", "mytool", srv.URL+"/bin/mytool", srv.URL+"/bin/mytool.sha256", "mytool"); err == nil {
		t.Fatal("ensureFile with mismatched checksum = nil error, want error")
	}

	if _, err := os.Stat(filepath.Join(c.dir, "mytool-v1-arm64", "mytool")); err == nil {
		t.Error("cached file was written despite checksum mismatch")
	}
}

func TestEnsureArchive_ExtractsPreservingStructure(t *testing.T) {
	t.Parallel()

	tarGz, checksum := buildTestTarGz(t, map[string]string{
		"bin/containerd":              "containerd binary",
		"bin/containerd-shim-runc-v2": "shim binary",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containerd.tar.gz":
			_, _ = w.Write(tarGz)
		case "/containerd.tar.gz.sha256sum":
			_, _ = w.Write([]byte(checksum + "  containerd.tar.gz"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCache(t.TempDir())
	ctx := context.Background()

	dir, err := c.ensureArchive(ctx, "containerd-v1-arm64", "containerd.tar.gz", srv.URL+"/containerd.tar.gz", srv.URL+"/containerd.tar.gz.sha256sum", "containerd.tar.gz")
	if err != nil {
		t.Fatalf("ensureArchive: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "bin", "containerd"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}

	if string(got) != "containerd binary" {
		t.Errorf("extracted content = %q, want %q", got, "containerd binary")
	}

	if _, err := os.Stat(filepath.Join(dir, "bin", "containerd-shim-runc-v2")); err != nil {
		t.Errorf("shim binary was not extracted alongside containerd: %v", err)
	}
}

func TestExtractTarGz_RejectsPathTraversal(t *testing.T) {
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

	if err := extractTarGz(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("extractTarGz with a path-traversal entry = nil error, want error")
	}
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)

	return sum[:]
}

// buildTestTarGz builds a gzip-compressed tar archive from files (path ->
// content) and returns its bytes and hex sha256 digest.
func buildTestTarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
			t.Fatalf("WriteHeader(%s): %v", name, err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	sum := sha256Sum(buf.Bytes())

	return buf.Bytes(), hex.EncodeToString(sum)
}
