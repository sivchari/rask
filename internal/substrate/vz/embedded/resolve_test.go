//go:build darwin

package embedded

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolve_EnvOverride proves $RASK_INIT_BINARY short-circuits the
// embedded-binary check entirely, real end to end.
func TestResolve_EnvOverride(t *testing.T) {
	want := []byte("a prebuilt local rask-init, for development")

	path := filepath.Join(t.TempDir(), "rask-init")
	if err := os.WriteFile(path, want, 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Setenv(RaskInitBinaryEnvVar, path)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

// TestResolve_EnvOverrideMissingFile proves a set-but-wrong
// $RASK_INIT_BINARY fails loudly instead of silently falling through to
// the embedded binary.
func TestResolve_EnvOverrideMissingFile(t *testing.T) {
	t.Setenv(RaskInitBinaryEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := Resolve(); err == nil {
		t.Fatal("Resolve with a missing $RASK_INIT_BINARY = nil error, want error")
	}
}

// TestResolve_UsesRealEmbeddedBinary proves Resolve returns RaskInitBinary
// directly once it is real — skipped if `make build-rask-init` has not run
// in this checkout, matching TestRaskInitBinary_IsRealELFNotPlaceholder's
// own convention.
func TestResolve_UsesRealEmbeddedBinary(t *testing.T) {
	if IsPlaceholder() {
		t.Skip("embedded/rask-init is still the placeholder; run `make build-rask-init` to cross-compile the real binary")
	}

	t.Setenv(RaskInitBinaryEnvVar, "")

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(RaskInitBinary) {
		t.Error("Resolve() did not return RaskInitBinary verbatim")
	}
}

// TestResolve_PlaceholderAndNoOverrideFails proves Resolve fails with a
// clear, actionable error (naming both escape hatches) rather than
// attempting a network download, when the embedded binary is still the
// placeholder and no override is set.
func TestResolve_PlaceholderAndNoOverrideFails(t *testing.T) {
	if !IsPlaceholder() {
		t.Skip("embedded/rask-init is a real cross-compiled binary in this checkout; this test only applies to the placeholder")
	}

	t.Setenv(RaskInitBinaryEnvVar, "")

	if _, err := Resolve(); err == nil {
		t.Fatal("Resolve() with the placeholder and no override = nil error, want error")
	}
}
