package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/pkg/cluster"
)

func newDeleteCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a rask resource",
	}

	cmd.AddCommand(newDeleteClusterCommand(rt, homeDir))

	return cmd
}

func newDeleteClusterCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Delete a cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deleteCluster(cmd.Context(), rt, homeDir, name)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")

	return cmd
}

// deleteCluster delegates to pkg/cluster.Provider.Delete — see
// create.go's createCluster doc comment for why cmd/rask routes through
// pkg/cluster rather than duplicating its logic.
func deleteCluster(ctx context.Context, rt substrate.Runtime, homeDir, name string) error {
	return cluster.NewProviderWithRuntime(rt, homeDir).Delete(ctx, name)
}
