//go:build darwin

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster, storing state under homeDir.
func newPlatformRuntime(homeDir string) substrate.Runtime {
	return vz.New(homeDir)
}
