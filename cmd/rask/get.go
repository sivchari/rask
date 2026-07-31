package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/pkg/cluster"
)

func newGetCommand(homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Display rask resources",
	}

	cmd.AddCommand(newGetClustersCommand(homeDir))

	return cmd
}

func newGetClustersCommand(homeDir string) *cobra.Command {
	return &cobra.Command{
		Use:   "clusters",
		Short: "List clusters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// get has no substrate.Runtime to inject a test double for
			// (List is pure directory listing); NewProvider's real
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
