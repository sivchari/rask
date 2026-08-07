package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/pkg/cluster"
)

func newGetCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Display rask resources",
	}

	cmd.AddCommand(newGetClustersCommand(homeDir))
	cmd.AddCommand(newGetPrebootPathCommand(rt, homeDir))

	return cmd
}

func newGetClustersCommand(homeDir string) *cobra.Command {
	return &cobra.Command{
		Use:   "clusters",
		Short: "List clusters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// List is pure directory listing and needs no
			// substrate.Runtime test double (unlike this file's
			// newGetPrebootPathCommand); NewProvider's real
			// platform-runtime selection is never exercised by this
			// command, so using it directly here (rather than
			// NewProviderWithRuntime, unlike every other cmd/rask
			// command) is safe in tests too.
			provider, err := cluster.NewProvider(homeDir)
			if err != nil {
				return err
			}

			names, err := provider.List()
			if err != nil {
				return err
			}

			if len(names) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No clusters found")

				return err
			}

			for _, name := range names {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

// newGetPrebootPathCommand fills the gap a CLI-only "rask create cluster
// --preboot-file" caller otherwise has no way to close: pkg/cluster.
// Provider.PrebootPath is substrate-independent (host path on hostproc,
// in-guest path on vz), but only Go library consumers could call it
// directly. This exposes the same computation on the command line.
func newGetPrebootPathCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	return &cobra.Command{
		Use:   "preboot-path CLUSTER DEST",
		Short: "Print the in-cluster absolute path a --preboot-file DEST resolves to",
		Long: `Print the absolute path an in-cluster component (typically kube-apiserver,
via --apiserver-arg) must use to read a file staged with "rask create cluster
--preboot-file SRC=DEST". The path is substrate-dependent (a host path on
hostproc, an in-guest path on vz), so this command computes the right one
instead of the caller guessing.

CLUSTER need not already exist: the path is computed, not looked up, so this
can run before "rask create cluster" to build its own --apiserver-arg value.`,
		Example: `  rask create cluster foo \
    --preboot-file ./webhook.yaml=auth/webhook.yaml \
    --apiserver-arg authentication-token-webhook-config-file=$(rask get preboot-path foo auth/webhook.yaml)`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := cluster.NewProviderWithRuntime(rt, homeDir).PrebootPath(args[0], args[1])

			_, err := fmt.Fprintln(cmd.OutOrStdout(), path)

			return err
		},
	}
}
