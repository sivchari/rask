//go:build darwin

package embedded

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeELF returns a minimal byte slice that passes ValidateRaskInit: real
// ELF magic followed by padding so it's also longer than placeholderMagic.
func fakeELF(tail string) []byte {
	return append([]byte("\x7fELF"), []byte(tail)...)
}

// withRaskInitBinary temporarily replaces the package-level RaskInitBinary
// var with data, restoring the original at test cleanup — lets a test pin
// Resolve's "real embedded binary" branch deterministically, independent of
// whether `make build-rask-init` has actually run in this checkout.
func withRaskInitBinary(t *testing.T, data []byte) {
	t.Helper()

	orig := RaskInitBinary
	RaskInitBinary = data
	t.Cleanup(func() { RaskInitBinary = orig })
}

func TestValidateRaskInit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{name: "valid ELF", data: fakeELF("padding-to-be-realistic"), wantErr: false},
		{name: "empty", data: nil, wantErr: true},
		{name: "placeholder", data: []byte(placeholderMagic), wantErr: true},
		{name: "too short to be ELF", data: []byte("\x7fEL"), wantErr: true},
		{name: "wrong magic", data: []byte("not-an-elf-file-at-all"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRaskInit(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRaskInit(%q) error = %v, wantErr %v", tt.data, err, tt.wantErr)
			}
		})
	}
}

func TestOverridePath_DerivedFromCacheDir(t *testing.T) {
	t.Parallel()

	got := OverridePath("/home/.rask/cache")
	want := filepath.Join("/home/.rask/cache", "rask-init-injected")

	if got != want {
		t.Errorf("OverridePath() = %q, want %q", got, want)
	}
}

// TestResolve_OverridePathTakesPriorityOverRealEmbeddedBinary proves the
// new resolution order: an explicit WithRaskInit injection (the override
// file) wins even when the embedded binary is also real, since the whole
// point of WithRaskInit is that a module consumer's own embedded binary
// (the one actually being run) may differ from whatever this rask commit's
// own embedded/rask-init happens to contain.
func TestResolve_OverridePathTakesPriorityOverRealEmbeddedBinary(t *testing.T) {
	withRaskInitBinary(t, fakeELF("real-embedded-binary"))
	t.Setenv(RaskInitBinaryEnvVar, "")

	want := fakeELF("injected-via-override-path")
	path := filepath.Join(t.TempDir(), "rask-init-injected")

	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got, err := Resolve(path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Resolve() = %q, want the override file's content %q", got, want)
	}
}

// TestResolve_InvalidOverrideFileFailsInsteadOfFallingThrough proves an
// invalid injected binary fails loudly and immediately rather than
// silently falling back to the embedded binary or $RASK_INIT_BINARY — an
// invalid injection must never degrade into a boot timeout instead of a
// clear error.
func TestResolve_InvalidOverrideFileFailsInsteadOfFallingThrough(t *testing.T) {
	withRaskInitBinary(t, fakeELF("real-embedded-binary-would-otherwise-be-used"))
	t.Setenv(RaskInitBinaryEnvVar, "")

	path := filepath.Join(t.TempDir(), "rask-init-injected")
	if err := os.WriteFile(path, []byte(placeholderMagic), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := Resolve(path)
	if err == nil {
		t.Fatal("Resolve with a placeholder override file = nil error, want error")
	}

	if !strings.Contains(err.Error(), path) {
		t.Errorf("Resolve error = %q, want it to name the invalid override path %q", err, path)
	}
}

// TestResolve_MissingOverrideFileFallsThrough proves a non-existent
// overridePath (the common case: no cluster.WithRaskInit was used) is
// silently skipped rather than treated as an error, so both
// Runtime.Create and RunVMHost can pass OverridePath's result
// unconditionally without knowing whether an injection actually happened.
func TestResolve_MissingOverrideFileFallsThrough(t *testing.T) {
	withRaskInitBinary(t, fakeELF("real-embedded-binary"))
	t.Setenv(RaskInitBinaryEnvVar, "")

	path := filepath.Join(t.TempDir(), "does-not-exist")

	got, err := Resolve(path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(RaskInitBinary) {
		t.Error("Resolve() with a missing override file did not fall through to RaskInitBinary")
	}
}

// TestResolve_EnvOverride proves $RASK_INIT_BINARY still works as the
// last-resort debug escape hatch when there is no override path and the
// embedded binary is the placeholder.
func TestResolve_EnvOverride(t *testing.T) {
	withRaskInitBinary(t, []byte(placeholderMagic))

	want := []byte("a prebuilt local rask-init, for development")

	path := filepath.Join(t.TempDir(), "rask-init")
	if err := os.WriteFile(path, want, 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Setenv(RaskInitBinaryEnvVar, path)

	got, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

// TestResolve_RealEmbeddedBinaryTakesPriorityOverEnvOverride proves the
// reordered priority versus the pre-WithRaskInit behavior: once the
// embedded binary is real, $RASK_INIT_BINARY no longer silently
// short-circuits it. $RASK_INIT_BINARY is a fallback for when the embedded
// binary is still the placeholder, not a universal override.
func TestResolve_RealEmbeddedBinaryTakesPriorityOverEnvOverride(t *testing.T) {
	want := fakeELF("real-embedded-binary")
	withRaskInitBinary(t, want)

	envPath := filepath.Join(t.TempDir(), "rask-init")
	if err := os.WriteFile(envPath, []byte("should not be used"), 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Setenv(RaskInitBinaryEnvVar, envPath)

	got, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Resolve() = %q, want the real embedded binary %q", got, want)
	}
}

// TestResolve_EnvOverrideMissingFile proves a set-but-wrong
// $RASK_INIT_BINARY fails loudly instead of silently falling through.
func TestResolve_EnvOverrideMissingFile(t *testing.T) {
	withRaskInitBinary(t, []byte(placeholderMagic))
	t.Setenv(RaskInitBinaryEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := Resolve(""); err == nil {
		t.Fatal("Resolve with a missing $RASK_INIT_BINARY = nil error, want error")
	}
}

// TestResolve_PlaceholderAndNoOverrideFails proves Resolve fails with a
// clear, actionable error (naming cluster.WithRaskInit and the go build
// line, plus the debug escape hatch) rather than attempting a network
// download, when nothing is configured at all.
func TestResolve_PlaceholderAndNoOverrideFails(t *testing.T) {
	withRaskInitBinary(t, []byte(placeholderMagic))
	t.Setenv(RaskInitBinaryEnvVar, "")

	_, err := Resolve("")
	if err == nil {
		t.Fatal("Resolve() with the placeholder and no override = nil error, want error")
	}

	if !strings.Contains(err.Error(), "WithRaskInit") {
		t.Errorf("Resolve error = %q, want it to name cluster.WithRaskInit", err)
	}

	if !strings.Contains(err.Error(), "go build") {
		t.Errorf("Resolve error = %q, want it to include the go build line", err)
	}

	if !strings.Contains(err.Error(), RaskInitBinaryEnvVar) {
		t.Errorf("Resolve error = %q, want it to name %s as the debug escape hatch", err, RaskInitBinaryEnvVar)
	}
}
