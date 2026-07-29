package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/substrate"
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

// deleteCluster removes the cluster's substrate instance and, only once
// that has succeeded, its local state directory.
func deleteCluster(ctx context.Context, rt substrate.Runtime, homeDir, name string) error {
	exists, err := cluster.Exists(homeDir, name)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("cluster %q does not exist", name)
	}

	if err := rt.Delete(ctx, name); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := os.RemoveAll(cluster.Dir(homeDir, name)); err != nil {
		return fmt.Errorf("removing state directory for cluster %q: %w", name, err)
	}

	return nil
}
