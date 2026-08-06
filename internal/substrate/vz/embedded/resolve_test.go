//go:build darwin

package embedded

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestReleaseVersionFromBuildInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		info        *debug.BuildInfo
		wantVersion string
		wantOK      bool
	}{
		{
			name: "rask is the main module, exact tag",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: raskModulePath, Version: "v0.1.4"},
			},
			wantVersion: "0.1.4",
			wantOK:      true,
		},
		{
			name: "rask is the main module, devel build",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: raskModulePath, Version: "(devel)"},
			},
			wantOK: false,
		},
		{
			name: "rask is a dependency, exact tag",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/sivchari/fjord", Version: "(devel)"},
				Deps: []*debug.Module{
					{Path: "github.com/sivchari/fjord/otherdep", Version: "v1.0.0"},
					{Path: raskModulePath, Version: "v0.1.4"},
				},
			},
			wantVersion: "0.1.4",
			wantOK:      true,
		},
		{
			name: "rask is a dependency, pseudo-version (no matching release)",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/sivchari/fjord", Version: "(devel)"},
				Deps: []*debug.Module{
					{Path: raskModulePath, Version: "v0.1.5-0.20260101120000-abcdef123456"},
				},
			},
			wantOK: false,
		},
		{
			name: "rask is not present at all",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/sivchari/fjord", Version: "v1.0.0"},
				Deps: []*debug.Module{
					{Path: "github.com/sivchari/fjord/otherdep", Version: "v1.0.0"},
				},
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			version, ok := releaseVersionFromBuildInfo(tt.info)
			if ok != tt.wantOK {
				t.Fatalf("releaseVersionFromBuildInfo() ok = %v, want %v", ok, tt.wantOK)
			}

			if ok && version != tt.wantVersion {
				t.Errorf("releaseVersionFromBuildInfo() version = %q, want %q", version, tt.wantVersion)
			}
		})
	}
}

// TestResolve_EnvOverride proves $RASK_INIT_BINARY short-circuits every
// other resolution step, real end to end: no embedded-binary state or
// build/version info is consulted at all when it is set.
func TestResolve_EnvOverride(t *testing.T) {
	want := []byte("a prebuilt local rask-init, for development")

	path := filepath.Join(t.TempDir(), "rask-init")
	if err := os.WriteFile(path, want, 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Setenv(RaskInitBinaryEnvVar, path)

	got, err := Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

// TestResolve_EnvOverrideMissingFile proves a set-but-wrong
// $RASK_INIT_BINARY fails loudly instead of silently falling through to
// the embedded binary or a download.
func TestResolve_EnvOverrideMissingFile(t *testing.T) {
	t.Setenv(RaskInitBinaryEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := Resolve(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Resolve with a missing $RASK_INIT_BINARY = nil error, want error")
	}
}

// TestResolve_UsesRealEmbeddedBinary proves Resolve returns RaskInitBinary
// directly (no download) once it is real — skipped if `make build-rask-init`
// has not run in this checkout, matching
// TestRaskInitBinary_IsRealELFNotPlaceholder's own convention.
func TestResolve_UsesRealEmbeddedBinary(t *testing.T) {
	if IsPlaceholder() {
		t.Skip("embedded/rask-init is still the placeholder; run `make build-rask-init` to cross-compile the real binary")
	}

	t.Setenv(RaskInitBinaryEnvVar, "")

	got, err := Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if string(got) != string(RaskInitBinary) {
		t.Error("Resolve() did not return RaskInitBinary verbatim")
	}
}
