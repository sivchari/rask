// Command rask creates and manages disposable local Kubernetes clusters.
package main

import (
	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/substrate"
)

// defaultContextFormat is the kubeconfig context name format applied by
// "rask export kubeconfig" unless --context-format overrides it.
// "kind-{name}" gives kind-compatible context names, which tools such as
// Tilt auto-trust.
const defaultContextFormat = "rask-{name}"

// defaultClusterName is the cluster name used when --name is not given.
const defaultClusterName = "rask"

// newRootCommand builds the rask command tree. rt is the substrate runtime
// for the current platform, and homeDir is the directory rask stores
// cluster state under (~/.rask in production, a temp directory in tests).
func newRootCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	root := &cobra.Command{
		Use:           "rask",
		Short:         "Disposable local Kubernetes clusters, an order of magnitude faster than kind.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String("context-format", defaultContextFormat,
		`kubeconfig context name format; "{name}" is replaced with the cluster name (use "kind-{name}" for kind compatibility)`)

	root.AddCommand(
		newCreateCommand(rt, homeDir),
		newDeleteCommand(rt, homeDir),
		newGetCommand(homeDir),
		newExportCommand(homeDir),
	)

	return root
}
