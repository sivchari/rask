package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/pkg/cluster"
)

func newExportCommand(homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export rask resources",
	}

	cmd.AddCommand(newExportKubeconfigCommand(homeDir))

	return cmd
}

func newExportKubeconfigCommand(homeDir string) *cobra.Command {
	var name, output string

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Export a cluster's kubeconfig",
		RunE: func(cmd *cobra.Command, _ []string) error {
			contextFormat, err := cmd.Flags().GetString("context-format")
			if err != nil {
				return err
			}

			return exportKubeconfig(cmd, homeDir, name, output, contextFormat)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")
	cmd.Flags().StringVar(&output, "output", "", "path to write the kubeconfig to (defaults to stdout)")

	return cmd
}

// exportKubeconfig delegates to pkg/cluster.Provider.KubeConfig — see
// create.go's createCluster doc comment for why cmd/rask routes through
// pkg/cluster rather than duplicating its logic. get.go has no
// substrate.Runtime to inject a test double for, so cluster.NewProvider
// (real platform selection) is safe here too — this command never touches
// a substrate.Runtime at all, only the already-written kubeconfig file.
func exportKubeconfig(cmd *cobra.Command, homeDir, name, output, contextFormat string) error {
	provider, err := cluster.NewProvider(homeDir)
	if err != nil {
		return err
	}

	data, err := provider.KubeConfig(name, cluster.ExportOptions{ContextFormat: contextFormat})
	if err != nil {
		return err
	}

	if output == "" {
		_, err := cmd.OutOrStdout().Write(data)

		return err
	}

	if err := os.WriteFile(output, data, 0o600); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", output, err)
	}

	return nil
}
