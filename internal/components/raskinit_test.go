package components

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRaskInitURLs(t *testing.T) {
	t.Parallel()

	if got, want := raskInitArchiveFilename("0.1.5"), "rask-init_0.1.5_linux_arm64.tar.gz"; got != want {
		t.Errorf("raskInitArchiveFilename = %q, want %q", got, want)
	}

	if got, want := raskInitURL("0.1.5"), "https://github.com/sivchari/rask/releases/download/v0.1.5/rask-init_0.1.5_linux_arm64.tar.gz"; got != want {
		t.Errorf("raskInitURL = %q, want %q", got, want)
	}

	if got, want := raskInitChecksumURL("0.1.5"), "https://github.com/sivchari/rask/releases/download/v0.1.5/rask_0.1.5_checksums.txt"; got != want {
		t.Errorf("raskInitChecksumURL = %q, want %q", got, want)
	}
}

// TestEnsureRaskInit_CacheHitSkipsDownload mirrors
// TestEnsureGuestKernel_CacheHitSkipsDownload: EnsureRaskInit's download URL
// is the real github.com release asset, unreachable from a unit test, so
// this proves the cache-hit path (the one downgrades/repeat runs actually
// take) does no network I/O rather than exercising the download itself —
// the download+verify+extract mechanics it shares with every other
// Ensure*/ensureArchive caller are already covered by
// TestEnsureArchive_ExtractsPreservingStructure and
// TestEnsureFile_ChecksumMismatchFails.
func TestEnsureRaskInit_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	c := NewCache(t.TempDir())

	dir := filepath.Join(c.dir, "rask-init", "0.1.5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "rask-init"), []byte("fake rask-init binary"), 0o755); err != nil {
		t.Fatalf("seeding rask-init: %v", err)
	}

	// c.client is http.DefaultClient with no server behind it reachable
	// from this test; a cache miss would fail on a real network call, so
	// success here proves the cache-hit path took priority.
	path, err := c.EnsureRaskInit(context.Background(), "0.1.5")
	if err != nil {
		t.Fatalf("EnsureRaskInit (cache hit): %v", err)
	}

	if want := filepath.Join(dir, "rask-init"); path != want {
		t.Errorf("EnsureRaskInit path = %s, want %s", path, want)
	}
}
