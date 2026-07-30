package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/cluster"
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
			names, err := cluster.List(homeDir)
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
