//go:build darwin

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster, storing state under homeDir. The
// rask CLI never injects a rask-init override (unlike pkg/cluster's own
// newPlatformRuntime, which threads a cluster.WithRaskInit consumer through
// to vz.New's second argument): `make build`/`make bundle` always
// cross-compile a real one into internal/substrate/vz/embedded before this
// binary is built.
func newPlatformRuntime(homeDir string) substrate.Runtime {
	return vz.New(homeDir, nil)
}
