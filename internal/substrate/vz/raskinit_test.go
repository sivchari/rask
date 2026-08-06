//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// fakeRaskInitELF returns a byte slice that passes embedded.ValidateRaskInit:
// real ELF magic, padded well past placeholderMagic's own length so it
// never accidentally matches the "too short to be the placeholder" check.
func fakeRaskInitELF(tail string) []byte {
	return append([]byte("\x7fELF"), []byte(tail+"-padding-well-past-the-placeholder-magic-length")...)
}

// TestSyncRaskInitOverride_WritesValidatedBytes proves a cluster.WithRaskInit
// injection (r.raskInit) is written to embedded.OverridePath(cache.Dir())
// exactly, so RunVMHost's later, separate-process embedded.Resolve call
// reads back the same bytes Runtime.Create was configured with.
func TestSyncRaskInitOverride_WritesValidatedBytes(t *testing.T) {
	t.Parallel()

	cache := components.DefaultCache(t.TempDir())
	want := fakeRaskInitELF("injected-bytes")
	r := New(t.TempDir(), want)

	path, err := r.syncRaskInitOverride(cache)
	if err != nil {
		t.Fatalf("syncRaskInitOverride: %v", err)
	}

	if wantPath := embedded.OverridePath(cache.Dir()); path != wantPath {
		t.Errorf("syncRaskInitOverride path = %q, want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written override: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("written override content = %q, want %q", got, want)
	}
}

// TestSyncRaskInitOverride_RejectsInvalidBytesImmediately proves an invalid
// injection fails synchronously, before anything is written, instead of
// producing a corrupt override file that would only surface as an opaque
// boot failure later.
func TestSyncRaskInitOverride_RejectsInvalidBytesImmediately(t *testing.T) {
	t.Parallel()

	cache := components.DefaultCache(t.TempDir())
	r := New(t.TempDir(), []byte("not a real binary"))

	if _, err := r.syncRaskInitOverride(cache); err == nil {
		t.Fatal("syncRaskInitOverride with invalid bytes = nil error, want error")
	}

	if _, err := os.Stat(embedded.OverridePath(cache.Dir())); !os.IsNotExist(err) {
		t.Errorf("syncRaskInitOverride left a file behind after rejecting invalid bytes: stat err = %v", err)
	}
}

// TestSyncRaskInitOverride_RemovesStaleFileWhenUnset proves a Runtime
// constructed without cluster.WithRaskInit (r.raskInit == nil) never
// silently reuses an override file left behind by an earlier Provider
// construction against the same homeDir's cache directory — otherwise a
// plain "rask create" run against a homeDir a library caller had
// previously used with WithRaskInit would boot an unexpected rask-init.
func TestSyncRaskInitOverride_RemovesStaleFileWhenUnset(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	cache := components.DefaultCache(cacheDir)

	stalePath := embedded.OverridePath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatalf("seeding cache dir: %v", err)
	}

	if err := os.WriteFile(stalePath, fakeRaskInitELF("stale-from-a-previous-run"), 0o644); err != nil {
		t.Fatalf("seeding stale override: %v", err)
	}

	r := New(t.TempDir(), nil)

	path, err := r.syncRaskInitOverride(cache)
	if err != nil {
		t.Fatalf("syncRaskInitOverride: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale override file still present after syncRaskInitOverride with r.raskInit == nil: stat err = %v", err)
	}
}

// TestSyncRaskInitOverride_UnsetIsNoopWhenNoStaleFileExists proves the
// common case (no cluster.WithRaskInit ever used against this homeDir)
// doesn't error just because there was never anything to remove.
func TestSyncRaskInitOverride_UnsetIsNoopWhenNoStaleFileExists(t *testing.T) {
	t.Parallel()

	cache := components.DefaultCache(t.TempDir())
	r := New(t.TempDir(), nil)

	if _, err := r.syncRaskInitOverride(cache); err != nil {
		t.Fatalf("syncRaskInitOverride: %v", err)
	}
}
