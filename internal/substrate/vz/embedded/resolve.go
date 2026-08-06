//go:build darwin

package embedded

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"

	"github.com/sivchari/rask/internal/components"
)

// RaskInitBinaryEnvVar names the environment variable that, if set,
// overrides every other Resolve step with a path to a real rask-init
// binary on disk. The escape hatch a local fjord/rask development setup
// needs: neither RaskInitBinary (never rebuilt for a module cache's
// read-only checkout) nor a downloaded release asset (no tag exists yet
// for an unreleased commit) can be real in that situation.
const RaskInitBinaryEnvVar = "RASK_INIT_BINARY"

// raskModulePath is this module's own path, used to find its version in
// runtime/debug.BuildInfo (as the main module, or as a dependency of
// whatever module is actually being built — e.g. fjord).
const raskModulePath = "github.com/sivchari/rask"

// exactReleaseTagVersion extracts the bare version (no leading "v") from a
// module version string, matching only an exact tagged release such as
// "v0.1.4" — not "(devel)" and not a pseudo-version such as
// "v0.1.5-0.20260101120000-abcdef123456", neither of which has a
// corresponding rask-init release asset to download.
var exactReleaseTagVersion = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

// Resolve returns the real linux/arm64 cmd/rask-init binary bytes rask
// should embed into a booting guest's initramfs, trying in order:
//
//  1. $RASK_INIT_BINARY, if set — read directly from disk, skipping every
//     other step.
//  2. RaskInitBinary, if this build actually cross-compiled it
//     (!IsPlaceholder()) — the common case for the rask CLI itself, which
//     `make build`/`make bundle` always cross-compile fresh (see the
//     Makefile).
//  3. A release download: if this rask module's own version (determined
//     via runtime/debug.ReadBuildInfo) is an exact tagged release
//     "vX.Y.Z", the matching rask-init_X.Y.Z_linux_arm64.tar.gz release
//     asset is downloaded, verified against that release's published
//     checksums file and cached under cacheDir/rask-init/X.Y.Z/rask-init
//     (internal/components.Cache.EnsureRaskInit) — the situation a Go
//     module consumer (e.g. fjord) is in: its module cache is read-only,
//     so RaskInitBinary can never be anything but the committed
//     placeholder there.
//
// Anything else (an unreleased commit, a replaced/forked module, no build
// info at all) has no release asset to download, so Resolve fails with an
// error naming every escape hatch instead.
func Resolve(ctx context.Context, cacheDir string) ([]byte, error) {
	if override := os.Getenv(RaskInitBinaryEnvVar); override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("embedded: reading $%s=%s: %w", RaskInitBinaryEnvVar, override, err)
		}

		return data, nil
	}

	if !IsPlaceholder() {
		return RaskInitBinary, nil
	}

	version, ok := releaseVersion()
	if !ok {
		return nil, fmt.Errorf(
			"embedded: internal/substrate/vz/embedded/rask-init is still the placeholder, and this build's own version is not an exact tagged rask release to download rask-init for; "+
				"run `make build-rask-init` in the rask source tree, depend on an exact tagged rask release (vX.Y.Z), or set $%s to a prebuilt linux/arm64 rask-init binary",
			RaskInitBinaryEnvVar,
		)
	}

	path, err := components.NewCache(cacheDir).EnsureRaskInit(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("embedded: %w (alternatively, run `make build-rask-init` or set $%s)", err, RaskInitBinaryEnvVar)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("embedded: reading cached rask-init %s: %w", version, err)
	}

	return data, nil
}

// releaseVersion reports this rask module's version via
// runtime/debug.ReadBuildInfo, with ok true only if it is an exact release
// tag.
func releaseVersion() (version string, ok bool) {
	info, available := debug.ReadBuildInfo()
	if !available {
		return "", false
	}

	return releaseVersionFromBuildInfo(info)
}

// releaseVersionFromBuildInfo is releaseVersion's logic, factored out so it
// can be unit-tested against a constructed *debug.BuildInfo instead of only
// against whatever built this test binary.
func releaseVersionFromBuildInfo(info *debug.BuildInfo) (version string, ok bool) {
	modVersion := ""

	if info.Main.Path == raskModulePath {
		modVersion = info.Main.Version
	} else {
		for _, dep := range info.Deps {
			if dep.Path == raskModulePath {
				modVersion = dep.Version

				break
			}
		}
	}

	m := exactReleaseTagVersion.FindStringSubmatch(modVersion)
	if m == nil {
		return "", false
	}

	return m[1], true
}
