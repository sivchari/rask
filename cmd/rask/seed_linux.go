//go:build linux

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/prebake"
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/hostproc"
)

// seedBuildClusterName is the reserved cluster name "rask seed build" uses
// for its throwaway control plane. Prefixed with double underscores, like
// the "__vm-host" hidden subcommand, to make an accidental collision with a
// user-chosen --name vanishingly unlikely; buildSeed still guards against
// it explicitly either way.
const seedBuildClusterName = "__rask-seed-build__"

// newSeedBuildCommand returns the "rask seed build" subcommand on Linux,
// backed by hostproc.Runtime.BuildSeed. rt is always a *hostproc.Runtime
// here (see substrate_linux.go's newPlatformRuntime); the type assertion
// exists only so newSeedCommand can stay platform-agnostic in its own
// signature (substrate.Runtime has no BuildSeed method — seed building is
// hostproc-specific, since a vz cluster's datastore lives inside its guest
// VM's own disk, not on a host-readable path — see vz.go's Start doc
// comment).
func newSeedBuildCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	hp, ok := rt.(*hostproc.Runtime)
	if !ok {
		return nil
	}

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a prebaked cluster-state seed for the current Kubernetes version and manifest bundle",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return buildSeed(cmd.Context(), hp, homeDir)
		},
	}

	return cmd
}

// buildSeed rejects a leftover throwaway cluster from a previous failed
// build up front (BuildSeed's own failure-cleanup should normally prevent
// this, but a killed-mid-run "rask seed build" process wouldn't get the
// chance to), then delegates to hostproc.Runtime.BuildSeed.
func buildSeed(ctx context.Context, hp *hostproc.Runtime, homeDir string) error {
	exists, err := cluster.Exists(homeDir, seedBuildClusterName)
	if err != nil {
		return err
	}

	if exists {
		return fmt.Errorf("a previous seed build left behind cluster %q; run `rask delete cluster --name %s` first", seedBuildClusterName, seedBuildClusterName)
	}

	outPath := prebake.Path(homeDir, components.DefaultK8sVersion)

	return hp.BuildSeed(ctx, seedBuildClusterName, outPath)
}
