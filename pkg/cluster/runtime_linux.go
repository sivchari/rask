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
//
// raskInit (a WithRaskInit consumer's injected bytes) is ignored:
// internal/substrate/hostproc runs every cluster component directly on the
// host and has no rask-init/initramfs concept for these bytes to feed.
func newPlatformRuntime(homeDir string, raskInit []byte) (substrate.Runtime, error) {
	_ = raskInit

	return hostproc.New(homeDir), nil
}
