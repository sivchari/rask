package main

import (
	"fmt"
	"os"

	internalcluster "github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/pkg/cluster"
)

func main() {
	// Must run before any of this program's own flag/subcommand parsing
	// (cobra's, below, included): a spawned "rask __vm-host" re-exec of
	// this exact binary (see internal/substrate/vz's package doc) needs to
	// land here, not in newRootCommand's cobra tree. rask is itself just an
	// ordinary pkg/cluster consumer for this purpose — see
	// cluster.RunVMHostIfRequested's doc comment for the full contract any
	// consumer must follow.
	if handled, err := cluster.RunVMHostIfRequested(); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		return
	}

	homeDir, err := internalcluster.DefaultHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := newRootCommand(newPlatformRuntime(homeDir), homeDir).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
