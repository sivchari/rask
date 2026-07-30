package components

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureE2fsprogsBundle_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	dir := filepath.Join(c.dir, "e2fsprogs-"+E2fsprogsBundleVersion)
	if err := os.MkdirAll(filepath.Join(dir, "sbin"), 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "sbin", "mke2fs"), []byte("prebuilt"), 0o755); err != nil {
		t.Fatalf("seeding marker file: %v", err)
	}

	// c.client has no reachable server for this test; a cache miss would
	// fail on the real network call, so success proves the cache-hit
	// path was taken.
	bundle, err := c.EnsureE2fsprogsBundle(context.Background())
	if err != nil {
		t.Fatalf("EnsureE2fsprogsBundle (cache hit): %v", err)
	}

	if bundle.Dir != dir {
		t.Errorf("Dir = %s, want %s", bundle.Dir, dir)
	}
}
