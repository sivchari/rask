package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
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

// exportKubeconfig reads the kubeconfig rask wrote for name at create time
// (where the cluster, user and context entries are all keyed by the
// cluster's plain name) and renames its context to match contextFormat.
func exportKubeconfig(cmd *cobra.Command, homeDir, name, output, contextFormat string) error {
	exists, err := cluster.Exists(homeDir, name)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("cluster %q does not exist", name)
	}

	kubeconfigPath := filepath.Join(cluster.Dir(homeDir, name), "kubeconfig")

	cfg, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("reading kubeconfig for cluster %q: %w", name, err)
	}

	contextName := cluster.FormatContext(contextFormat, name)
	if contextName != name {
		ctx, ok := cfg.Contexts[name]
		if !ok {
			return fmt.Errorf("kubeconfig for cluster %q has no context named %q", name, name)
		}

		cfg.Contexts[contextName] = ctx
		delete(cfg.Contexts, name)

		if cfg.CurrentContext == name {
			cfg.CurrentContext = contextName
		}
	}

	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return fmt.Errorf("rendering kubeconfig for cluster %q: %w", name, err)
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
