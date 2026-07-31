package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/pkg/cluster"
)

// apiAudienceFlag is the --api-audience flag name, repeatable so callers
// can request more than one extra TokenReview audience (e.g. haro's
// projected ServiceAccount token uses "haro").
const apiAudienceFlag = "api-audience"

// apiserverArgFlag, prebootFileFlag, componentDirFlag and coreDNSImageFlag
// are the fjord-integration seam flags: a caller-supplied kube-apiserver
// flag (kubeadm-style), a file to place into the cluster's data directory
// before any process starts, a local directory of pre-extracted core
// Kubernetes binaries, and a CoreDNS image override, respectively. Each maps
// directly onto the like-named pkg/cluster.Options field.
const (
	apiserverArgFlag = "apiserver-arg"
	prebootFileFlag  = "preboot-file"
	componentDirFlag = "component-dir"
	coreDNSImageFlag = "coredns-image"
)

func newCreateCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a rask resource",
	}

	cmd.AddCommand(newCreateClusterCommand(rt, homeDir))

	return cmd
}

// createClusterFlags holds every "rask create cluster" flag value, passed
// as one unit from RunE to createCluster to keep both signatures from
// growing a parameter per flag.
type createClusterFlags struct {
	name          string
	wait          string
	verbose       bool
	apiAudiences  []string
	apiserverArgs []string
	prebootFiles  []string
	componentDir  string
	coreDNSImage  string
}

func newCreateClusterCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var flags createClusterFlags

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create a new cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return createCluster(cmd, rt, homeDir, flags)
		},
	}

	cmd.Flags().StringVar(&flags.name, "name", defaultClusterName, "cluster name")
	cmd.Flags().StringVar(&flags.wait, "wait", cluster.WaitNode, `what to wait for before returning: "node" or "coredns"`)
	cmd.Flags().BoolVar(&flags.verbose, "verbose", false, "print a phase-by-phase boot latency breakdown")
	cmd.Flags().StringArrayVar(&flags.apiAudiences, apiAudienceFlag, nil,
		`extra apiserver --api-audiences value beyond the cluster's own service-account issuer (repeatable), for TokenReview clients that request a custom audience (e.g. --api-audience haro)`)
	cmd.Flags().StringArrayVar(&flags.apiserverArgs, apiserverArgFlag, nil,
		`extra kube-apiserver flag as key=value (repeatable, kubeadm-style, no leading "--"), appended after rask's own flags; a key naming a rask-managed flag is rejected`)
	cmd.Flags().StringArrayVar(&flags.prebootFiles, prebootFileFlag, nil,
		`file to place into the cluster's data directory before any process starts, as src=dest (repeatable); dest is relative to <data-dir>/preboot (see pkg/cluster.PrebootFile)`)
	cmd.Flags().StringVar(&flags.componentDir, componentDirFlag, "",
		"local directory containing pre-extracted kube-apiserver/kube-controller-manager/kube-scheduler/kubelet/kubectl binaries, used instead of rask's default dl.k8s.io download cache")
	cmd.Flags().StringVar(&flags.coreDNSImage, coreDNSImageFlag, "",
		fmt.Sprintf("CoreDNS image to use instead of rask's default (%s)", manifests.CoreDNSImage))

	return cmd
}

// createCluster delegates to pkg/cluster.Provider.Create, which is the
// single implementation of "create a cluster" both this CLI and any Go
// library consumer (e.g. fjord) go through — see that package's doc comment.
// rt is wrapped via cluster.NewProviderWithRuntime rather than
// cluster.NewProvider so this command keeps using the same
// platform-selected (or, in tests, fake) substrate.Runtime the rest of
// cmd/rask already shares.
func createCluster(cmd *cobra.Command, rt substrate.Runtime, homeDir string, flags createClusterFlags) error {
	prebootFiles, err := parsePrebootFiles(flags.prebootFiles)
	if err != nil {
		return err
	}

	provider := cluster.NewProviderWithRuntime(rt, homeDir)

	result, err := provider.Create(cmd.Context(), flags.name, cluster.Options{
		Wait:               flags.wait,
		ExtraAPIAudiences:  flags.apiAudiences,
		ExtraAPIServerArgs: flags.apiserverArgs,
		PrebootFiles:       prebootFiles,
		ComponentDir:       flags.componentDir,
		CoreDNSImage:       flags.coreDNSImage,
	})
	if err != nil {
		return err
	}

	if flags.verbose && result.Timeline != "" {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), result.Timeline)
	}

	return nil
}

// parsePrebootFiles parses every --preboot-file "src=dest" flag value into
// a cluster.PrebootFile.
func parsePrebootFiles(raw []string) ([]cluster.PrebootFile, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	files := make([]cluster.PrebootFile, 0, len(raw))

	for _, r := range raw {
		src, dest, ok := strings.Cut(r, "=")
		if !ok || src == "" || dest == "" {
			return nil, fmt.Errorf("invalid --%s %q: must be src=dest", prebootFileFlag, r)
		}

		files = append(files, cluster.PrebootFile{Src: src, Dest: dest})
	}

	return files, nil
}
