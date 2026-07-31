//go:build darwin

package cluster

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster, storing state under homeDir.
// Mirrors cmd/rask/substrate_darwin.go's newPlatformRuntime — the CLI and
// this package must select the identical backend, or their behavior would
// diverge.
func newPlatformRuntime(homeDir string) substrate.Runtime {
	return vz.New(homeDir)
}
