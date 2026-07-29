package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/substrate"
)

func newCreateCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a rask resource",
	}

	cmd.AddCommand(newCreateClusterCommand(rt, homeDir))

	return cmd
}

func newCreateClusterCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create a new cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return createCluster(cmd.Context(), rt, homeDir, name)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")

	return cmd
}

// createCluster creates the cluster's substrate instance and, only once
// that has fully succeeded, its local state directory. This keeps a failed
// create (expected while the substrate is a stub) from leaving behind state
// that would make a retry think the cluster already exists.
func createCluster(ctx context.Context, rt substrate.Runtime, homeDir, name string) error {
	exists, err := cluster.Exists(homeDir, name)
	if err != nil {
		return err
	}

	if exists {
		return fmt.Errorf("cluster %q already exists", name)
	}

	if err := rt.Create(ctx, name); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := rt.Start(ctx, name); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := os.MkdirAll(cluster.Dir(homeDir, name), 0o755); err != nil {
		return fmt.Errorf("creating state directory for cluster %q: %w", name, err)
	}

	return nil
}
