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
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Cache downloads and caches component binaries under a version- and
// architecture-scoped directory tree. A Cache is safe for concurrent use.
type Cache struct {
	dir    string
	client *http.Client
}

// NewCache returns a Cache rooted at dir (typically ~/.rask/cache).
func NewCache(dir string) *Cache {
	return &Cache{dir: dir, client: http.DefaultClient}
}

// Dir returns the root directory this Cache is scoped to, for callers that
// need to place their own cache entries alongside the component-scoped
// subdirectories Ensure/EnsureGuestKernel/etc. create (e.g.
// internal/substrate/vz's built initramfs).
func (c *Cache) Dir() string {
	return c.dir
}

// ensureFile downloads url into c.dir/subdir/filename if it is not already
// present, verifying it against the sha256 digest found in checksumURL
// (matched by checksumFilename) before it is considered cached. It returns
// the absolute path to the cached file.
func (c *Cache) ensureFile(ctx context.Context, subdir, filename, url, checksumURL, checksumFilename string) (string, error) {
	dir := filepath.Join(c.dir, subdir)
	dest := filepath.Join(dir, filename)

	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("components: creating cache dir %s: %w", dir, err)
	}

	data, err := c.fetch(ctx, url)
	if err != nil {
		return "", fmt.Errorf("components: downloading %s: %w", url, err)
	}

	if err := c.verify(ctx, data, checksumURL, checksumFilename); err != nil {
		return "", fmt.Errorf("components: verifying %s: %w", url, err)
	}

	if err := writeExecutable(dest, data); err != nil {
		return "", err
	}

	return dest, nil
}

// ensureArchive downloads and extracts a tar.gz archive from url into
// c.dir/subdir if that directory does not already exist, verifying it
// against checksumURL first. It returns the absolute path to the extraction
// directory.
func (c *Cache) ensureArchive(ctx context.Context, subdir, archiveFilename, url, checksumURL, checksumFilename string) (string, error) {
	dir := filepath.Join(c.dir, subdir)

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	data, err := c.fetch(ctx, url)
	if err != nil {
		return "", fmt.Errorf("components: downloading %s: %w", url, err)
	}

	if err := c.verify(ctx, data, checksumURL, checksumFilename); err != nil {
		return "", fmt.Errorf("components: verifying %s: %w", url, err)
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return "", fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	if err := extractTarGz(data, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)

		return "", fmt.Errorf("components: extracting %s: %w", archiveFilename, err)
	}

	if err := os.Rename(tmpDir, dir); err != nil {
		return "", fmt.Errorf("components: finalizing %s: %w", dir, err)
	}

	return dir, nil
}

func (c *Cache) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func (c *Cache) verify(ctx context.Context, data []byte, checksumURL, checksumFilename string) error {
	checksumBody, err := c.fetch(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("fetching checksum %s: %w", checksumURL, err)
	}

	want, err := parseChecksum(checksumBody, checksumFilename)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])

	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}

	return nil
}

func writeExecutable(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return fmt.Errorf("components: writing %s: %w", path, err)
	}

	return nil
}

// extractTarGz extracts a gzip-compressed tar archive into destDir,
// preserving the archive's internal directory structure (needed so
// containerd's shim binaries end up alongside the containerd binary
// itself). Entries that would extract outside destDir are rejected.
func extractTarGz(data []byte, destDir string) error {
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
		default:
			// Symlinks and other special entries are not present in
			// rask's pinned containerd/cni-plugins releases; skip
			// anything unexpected rather than failing the whole
			// extraction.
		}
	}
}

func extractFile(r io.Reader, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", target, err)
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()

		return fmt.Errorf("writing %s: %w", target, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", target, err)
	}

	return nil
}

// safeJoin joins destDir with the tar entry name, rejecting entries (via an
// absolute path or a "../" segment) that would escape destDir. Archives
// come from the network, so this is an external-input boundary check, not a
// hypothetical concern.
func safeJoin(destDir, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("components: tar entry %q is an absolute path", name)
	}

	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("components: tar entry %q escapes destination directory", name)
	}

	return filepath.Join(destDir, cleaned), nil
}
