//go:build darwin

package embedded

import (
	"fmt"
	"os"
	"path/filepath"
)

// RaskInitBinaryEnvVar names the environment variable that, if set,
// overrides Resolve's embedded-binary step with a path to a real rask-init
// binary on disk. A debug escape hatch for local development against an
// unreleased rask commit: prefer cluster.WithRaskInit (see this package's
// doc comment) for anything checked in, since it is validated immediately
// at Provider construction time instead of only once a VM tries to boot.
const RaskInitBinaryEnvVar = "RASK_INIT_BINARY"

// overrideFileName is OverridePath's file name, kept as a constant so
// Resolve and OverridePath can never disagree on it.
const overrideFileName = "rask-init-injected"

// OverridePath returns the deterministic path Resolve checks first: where
// Runtime.Create (internal/substrate/vz/vz.go's syncRaskInitOverride)
// writes a pkg/cluster consumer's cluster.WithRaskInit bytes, rooted under
// cacheDir (a *components.Cache's own Dir(), already homeDir-scoped) rather
// than under homeDir directly so it sits alongside the initramfs cache
// entry it feeds.
//
// This file, not an environment variable or a new CLI flag, is what hands
// the injected bytes from the process that called Runtime.Create to the
// separate "rask __vm-host" process that later calls buildTemplateInitramfs
// again (see vmhost.go's RunVMHost): the two share a filesystem (via
// homeDir) but not memory, and both can derive this exact path from homeDir
// alone with no extra state to pass across that boundary.
func OverridePath(cacheDir string) string {
	return filepath.Join(cacheDir, overrideFileName)
}

// ValidateRaskInit reports whether data looks like a real linux/arm64
// rask-init binary rask can embed into a booting guest's initramfs: non-
// empty, not this package's own placeholder content, and starting with the
// ELF magic number. Exported so pkg/cluster can reject an invalid
// cluster.WithRaskInit injection immediately at Provider construction time
// — an invalid injection must never be discovered only once a VM boot times
// out — and so Resolve can apply the identical check when reading
// OverridePath back in a separate process.
func ValidateRaskInit(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("rask-init is empty")
	}

	if isPlaceholderBytes(data) {
		return fmt.Errorf("rask-init is the placeholder, not a real binary")
	}

	if len(data) < 4 || string(data[:4]) != "\x7fELF" {
		return fmt.Errorf("rask-init does not start with an ELF magic number")
	}

	return nil
}

// Resolve returns the real linux/arm64 cmd/rask-init binary bytes rask
// should embed into a booting guest's initramfs, trying in order:
//
//  1. overridePath, if non-empty and the file exists — where a
//     cluster.WithRaskInit injection was written (see OverridePath).
//     Validated with ValidateRaskInit; an invalid file fails loudly instead
//     of silently falling through to the next step, since a caller that
//     went to the trouble of injecting a binary almost certainly wants an
//     error, not a silent fallback to a possibly-wrong one.
//  2. RaskInitBinary, if this build actually cross-compiled it
//     (!IsPlaceholder()) — the common case for the rask CLI itself, which
//     `make build`/`make bundle` always cross-compile fresh (see the
//     Makefile).
//  3. $RASK_INIT_BINARY, if set — read directly from disk. A debug escape
//     hatch, not the supported route for a Go module consumer: see
//     RaskInitBinaryEnvVar's doc comment.
//
// Every rask release is bundled (see internal/components/bundlepayload):
// there is no slim release binary, and therefore no rask-init release asset
// to fall back to downloading — every real build path either cross-compiles
// rask-init itself, or is a Go module consumer whose module cache is
// read-only (e.g. fjord's), which must supply cluster.WithRaskInit (or, for
// local development only, $RASK_INIT_BINARY). Anything else (a build that
// never ran `make build-rask-init`, never called WithRaskInit, and has no
// override env set) has no real rask-init to embed, so Resolve fails with
// an error naming the supported route and both escape hatches instead.
func Resolve(overridePath string) ([]byte, error) {
	if overridePath != "" {
		data, err := os.ReadFile(overridePath)

		switch {
		case err == nil:
			if verr := ValidateRaskInit(data); verr != nil {
				return nil, fmt.Errorf("embedded: %s (written by cluster.WithRaskInit) is invalid: %w", overridePath, verr)
			}

			return data, nil
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("embedded: reading %s: %w", overridePath, err)
		}
	}

	if !IsPlaceholder() {
		return RaskInitBinary, nil
	}

	if override := os.Getenv(RaskInitBinaryEnvVar); override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("embedded: reading $%s=%s: %w", RaskInitBinaryEnvVar, override, err)
		}

		return data, nil
	}

	return nil, fmt.Errorf(
		"embedded: no rask-init binary available. If you are consuming rask as a Go module, cross-compile one at build time and inject it with cluster.WithRaskInit:\n"+
			"  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o internal/embedded/rask-init github.com/sivchari/rask/cmd/rask-init\n"+
			"If you are building rask itself, run `make build-rask-init` in the rask source tree.\n"+
			"(debug only) $%s may also point at a prebuilt linux/arm64 rask-init binary",
		RaskInitBinaryEnvVar,
	)
}
