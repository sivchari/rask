//go:build linux

package main

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/hostproc"
)

// newPlatformRuntime returns the substrate.Runtime for Linux: supervised
// host processes, storing cluster state under homeDir.
func newPlatformRuntime(homeDir string) substrate.Runtime {
	return hostproc.New(homeDir)
}
