//go:build linux

package main

import "github.com/spf13/cobra"

// newVMHostCommand returns nil on Linux: the "__vm-host" subcommand only
// exists to back internal/substrate/vz's macOS VM lifecycle (see
// vmhost_darwin.go); internal/substrate/hostproc has no equivalent
// detached-process model to wire up.
func newVMHostCommand() *cobra.Command {
	return nil
}
