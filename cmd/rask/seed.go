package main

import (
	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate"
)

// newSeedCommand returns the "rask seed" command group, or nil if no
// subcommand is available on this platform (mirroring newVMHostCommand's
// nil-on-unsupported-platform convention).
func newSeedCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	build := newSeedBuildCommand(rt, homeDir)
	if build == nil {
		return nil
	}

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Manage prebaked cluster-state seeds",
	}

	cmd.AddCommand(build)

	return cmd
}
