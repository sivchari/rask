//go:build darwin

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster.
func newPlatformRuntime() substrate.Runtime {
	return vz.New()
}
