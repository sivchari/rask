package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/manifests"
)

// addonInstaller applies one addon's manifest against a cluster.
type addonInstaller func(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper) error

// addonInstallers maps an addon name (as passed to "rask addon install")
// to the manifests package function that applies it. Both addons here are
// CRD-only bundles (see internal/manifests/gatewayapi.go and snapshot.go):
// installing them makes the corresponding types available to apply
// against, without running any controller for them.
var addonInstallers = map[string]addonInstaller{
	"gateway-api":   manifests.ApplyGatewayAPICRDs,
	"snapshot-crds": manifests.ApplySnapshotCRDs,
}

func newAddonCommand(homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "addon",
		Short: "Manage optional cluster addons",
	}

	cmd.AddCommand(newAddonInstallCommand(homeDir))

	return cmd
}

func newAddonInstallCommand(homeDir string) *cobra.Command {
	var clusterFlagName string

	cmd := &cobra.Command{
		Use:   "install <addon>",
		Short: "Install an addon's CRDs into a cluster (gateway-api, snapshot-crds)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			install, ok := addonInstallers[args[0]]
			if !ok {
				return fmt.Errorf("unknown addon %q: must be one of %q, %q", args[0], "gateway-api", "snapshot-crds")
			}

			return installAddon(cmd.Context(), homeDir, clusterFlagName, install)
		},
	}

	cmd.Flags().StringVar(&clusterFlagName, "name", defaultClusterName, "cluster name")

	return cmd
}

func installAddon(ctx context.Context, homeDir, clusterName string, install addonInstaller) error {
	kubeconfigPath := filepath.Join(cluster.Dir(homeDir, clusterName), "kubeconfig")

	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("loading kubeconfig for cluster %q: %w", clusterName, err)
	}

	_, dyn, mapper, err := manifests.BuildClients(restConfig)
	if err != nil {
		return err
	}

	return install(ctx, dyn, mapper)
}
