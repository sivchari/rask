//go:build linux

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/hostproc"
)

// newPlatformRuntime returns the substrate.Runtime for Linux: supervised
// host processes in a dedicated network namespace.
func newPlatformRuntime() substrate.Runtime {
	return hostproc.New()
}
