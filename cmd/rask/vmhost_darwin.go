//go:build darwin

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate/vz"
)

// vmHostSignals are the signals that trigger vm-host's graceful shutdown
// path (context cancellation, unblocking RunVMHost's runCtx.Done() case so
// its deferred stopVM/console.Close/net.Close/lock.Release actually run).
//
// SIGHUP is included alongside the obvious SIGTERM/SIGINT as defense in
// depth: internal/substrate/vz.Runtime's spawnVMHost now detaches this
// process into its own session (Setsid) specifically so it is no longer
// reachable by its spawning session's own signal delivery, but Setsid
// cannot protect against a SIGHUP sent directly to this process (e.g. an
// operator's own "kill -HUP", or any other future caller of syscall.Kill).
// Go's default disposition for an unhandled SIGHUP is to terminate the
// process immediately — no deferred cleanup, no log line — which is
// exactly the silent-death shape a real incident had (see spawnVMHost's
// doc comment for the full incident writeup). Catching it here routes it
// through the same clean shutdown as SIGTERM/SIGINT instead.
var vmHostSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

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
			ctx, cancel := signal.NotifyContext(cmd.Context(), vmHostSignals...)
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
