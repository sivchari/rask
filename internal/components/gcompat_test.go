package components

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureGCompatBundle_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	dir := filepath.Join(c.dir, "gcompat-"+GCompatBundleVersion)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "lib", "ld-linux-aarch64.so.1"), []byte("prebuilt"), 0o755); err != nil {
		t.Fatalf("seeding marker file: %v", err)
	}

	// c.client has no reachable server for this test; a cache miss would
	// fail on the real network call, so success proves the cache-hit
	// path was taken.
	bundle, err := c.EnsureGCompatBundle(context.Background())
	if err != nil {
		t.Fatalf("EnsureGCompatBundle (cache hit): %v", err)
	}

	if bundle.Dir != dir {
		t.Errorf("Dir = %s, want %s", bundle.Dir, dir)
	}
}
