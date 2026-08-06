//go:build darwin

package cluster

import (
	"fmt"

	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster, storing state under homeDir.
// Mirrors cmd/rask/substrate_darwin.go's newPlatformRuntime (which always
// passes a nil raskInit — the CLI cross-compiles a real embedded binary at
// build time) — the CLI and this package must select the identical
// backend, or their behavior would diverge.
//
// raskInit, if non-nil, is a WithRaskInit consumer's injected bytes:
// validated here so an invalid injection is reported as a NewProvider
// construction error immediately, rather than only once a VM boot times
// out (vz.Runtime re-validates before every Create as its own defense in
// depth — see syncRaskInitOverride).
func newPlatformRuntime(homeDir string, raskInit []byte) (substrate.Runtime, error) {
	if raskInit != nil {
		if err := embedded.ValidateRaskInit(raskInit); err != nil {
			return nil, fmt.Errorf("cluster: invalid WithRaskInit bytes: %w", err)
		}
	}

	return vz.New(homeDir, raskInit), nil
}
