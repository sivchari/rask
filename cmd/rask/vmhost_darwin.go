//go:build darwin

package main

import (
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate/vz"
)

// newVMHostCommand returns the hidden "rask __vm-host" subcommand: the
// entry point for the detached child process internal/substrate/vz.Runtime.Start
// spawns to own one cluster's VM (see that package's doc comment). Not
// meant to be invoked directly by a user; hidden from --help and excluded
// from shell completions.
func newVMHostCommand() *cobra.Command {
	var home, name string

	cmd := &cobra.Command{
		Use:    "__vm-host",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()

			return vz.RunVMHost(ctx, home, name)
		},
	}

	cmd.Flags().StringVar(&home, "home", "", "rask home directory")
	cmd.Flags().StringVar(&name, "name", "", "cluster name")

	_ = cmd.MarkFlagRequired("home")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}
