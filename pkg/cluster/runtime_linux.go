//go:build linux

package cluster

import (
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/hostproc"
)

// newPlatformRuntime returns the substrate.Runtime for Linux: supervised
// host processes, storing cluster state under homeDir. Mirrors
// cmd/rask/substrate_linux.go's newPlatformRuntime — the CLI and this
// package must select the identical backend, or their behavior would
// diverge.
func newPlatformRuntime(homeDir string) substrate.Runtime {
	return hostproc.New(homeDir)
}
