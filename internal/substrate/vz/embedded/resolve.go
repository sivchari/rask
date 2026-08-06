//go:build darwin

package embedded

import (
	"fmt"
	"os"
)

// RaskInitBinaryEnvVar names the environment variable that, if set,
// overrides every other Resolve step with a path to a real rask-init
// binary on disk. The escape hatch a local fjord/rask development setup
// needs: RaskInitBinary is never rebuilt for a module cache's read-only
// checkout, so it can never be real in that situation.
const RaskInitBinaryEnvVar = "RASK_INIT_BINARY"

// Resolve returns the real linux/arm64 cmd/rask-init binary bytes rask
// should embed into a booting guest's initramfs, trying in order:
//
//  1. $RASK_INIT_BINARY, if set — read directly from disk, skipping the
//     next step.
//  2. RaskInitBinary, if this build actually cross-compiled it
//     (!IsPlaceholder()) — the common case for the rask CLI itself, which
//     `make build`/`make bundle` always cross-compile fresh (see the
//     Makefile).
//
// Every rask release is bundled (see internal/components/bundlepayload):
// there is no slim release binary, and therefore no rask-init release asset
// to fall back to downloading — every real build path either cross-compiles
// rask-init itself or is a Go module consumer whose module cache is
// read-only (e.g. fjord's), which must use $RASK_INIT_BINARY during local
// development against an unreleased rask commit. Anything else (a build
// that never ran `make build-rask-init` and has no override set) has no
// real rask-init to embed, so Resolve fails with an error naming both
// escape hatches instead.
func Resolve() ([]byte, error) {
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

	return nil, fmt.Errorf(
		"embedded: internal/substrate/vz/embedded/rask-init is still the placeholder; "+
			"run `make build-rask-init` in the rask source tree, or set $%s to a prebuilt linux/arm64 rask-init binary",
		RaskInitBinaryEnvVar,
	)
}
