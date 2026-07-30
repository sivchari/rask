package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/prebake"
	"github.com/sivchari/rask/internal/substrate"
)

// apiAudienceFlag is the --api-audience flag name, repeatable so callers
// can request more than one extra TokenReview audience (e.g. haro's
// projected ServiceAccount token uses "haro").
const apiAudienceFlag = "api-audience"

// waitNode and waitCoreDNS are the allowed --wait values.
const (
	waitNode    = "node"
	waitCoreDNS = "coredns"
)

// coreDNSWaitTimeout bounds --wait=coredns so a broken cluster fails fast
// instead of hanging "rask create" forever.
const coreDNSWaitTimeout = 60 * time.Second

func newCreateCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a rask resource",
	}

	cmd.AddCommand(newCreateClusterCommand(rt, homeDir))

	return cmd
}

func newCreateClusterCommand(rt substrate.Runtime, homeDir string) *cobra.Command {
	var (
		name         string
		wait         string
		verbose      bool
		apiAudiences []string
	)

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create a new cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return createCluster(cmd, rt, homeDir, name, wait, verbose, apiAudiences)
		},
	}

	cmd.Flags().StringVar(&name, "name", defaultClusterName, "cluster name")
	cmd.Flags().StringVar(&wait, "wait", waitNode, `what to wait for before returning: "node" or "coredns"`)
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print a phase-by-phase boot latency breakdown")
	cmd.Flags().StringArrayVar(&apiAudiences, apiAudienceFlag, nil,
		`extra apiserver --api-audiences value beyond the cluster's own service-account issuer (repeatable), for TokenReview clients that request a custom audience (e.g. --api-audience haro)`)

	return cmd
}

// createCluster creates the cluster's substrate instance and, only once
// that has fully succeeded, its local state directory. This keeps a failed
// create (expected while the substrate is a stub) from leaving behind state
// that would make a retry think the cluster already exists.
func createCluster(cmd *cobra.Command, rt substrate.Runtime, homeDir, name, wait string, verbose bool, apiAudiences []string) error {
	if wait != waitNode && wait != waitCoreDNS {
		return fmt.Errorf(`invalid --wait %q: must be %q or %q`, wait, waitNode, waitCoreDNS)
	}

	ctx := cmd.Context()

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

	// Start blocks until the node is Ready internally (see
	// internal/bootstrap.Boot), which is what satisfies the "node"
	// (default) --wait value; nothing further is needed for it here.
	opts := substrate.StartOptions{ExtraAPIAudiences: apiAudiences}

	// A seed matching the exact Kubernetes version and default manifest
	// bundle this build ships (internal/prebake.Key) is used automatically
	// when present, with no flag needed: it's a pure optimization a
	// substrate implementation applies before booting (see
	// internal/substrate/hostproc.Runtime.Start), never a behavior change,
	// so there's nothing for a caller to opt into.
	if seedPath := prebake.Path(homeDir, components.DefaultK8sVersion); fileExists(seedPath) {
		opts.SeedPath = seedPath
	}

	if err := rt.Start(ctx, name, opts); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := os.MkdirAll(cluster.Dir(homeDir, name), 0o755); err != nil {
		return fmt.Errorf("creating state directory for cluster %q: %w", name, err)
	}

	if wait == waitCoreDNS {
		if err := waitForCoreDNS(ctx, homeDir, name); err != nil {
			return fmt.Errorf("cluster %q: waiting for CoreDNS: %w", name, err)
		}
	}

	if verbose {
		printTimeline(cmd, homeDir, name)
	}

	return nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

// waitForCoreDNS polls the CoreDNS Deployment (applied by
// internal/manifests during Start) until it reports at least one Ready
// replica.
func waitForCoreDNS(ctx context.Context, homeDir, name string) error {
	kubeconfigPath := filepath.Join(cluster.Dir(homeDir, name), "kubeconfig")

	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, coreDNSWaitTimeout)
	defer cancel()

	return manifests.WaitDeploymentReady(waitCtx, clientset, "kube-system", "coredns")
}

// printTimeline prints the phase-by-phase boot latency breakdown a
// substrate implementation may have written (see
// internal/substrate/hostproc's timelinePath). Best-effort: silently does
// nothing if absent, since not every substrate necessarily produces one.
func printTimeline(cmd *cobra.Command, homeDir, name string) {
	path := filepath.Join(cluster.Dir(homeDir, name), "data", "timeline.txt")

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
}
