//go:build darwin

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz"
)

// newPlatformRuntime returns the substrate.Runtime for macOS: one
// Virtualization.framework VM per cluster. vz.New() does not yet take
// homeDir (M1's vz substrate is still a stub); accepted here only to keep
// this file's signature symmetric with substrate_linux.go's.
func newPlatformRuntime(_ string) substrate.Runtime {
	return vz.New()
}
