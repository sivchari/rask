//go:build darwin

package main

import (
	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate"
)

// newSeedBuildCommand returns nil on macOS: seed building has no vz
// equivalent yet. A vz cluster's datastore lives inside its guest VM's own
// disk, not on a host-readable path, so there's no file to extract a seed
// snapshot from without a new guest-side extraction mechanism — see
// vz.go's Start doc comment.
func newSeedBuildCommand(_ substrate.Runtime, _ string) *cobra.Command {
	return nil
}
