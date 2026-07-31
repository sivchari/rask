//go:build linux

package hostproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/substrate"
)

// buildSeedManifestSettleTimeout bounds how long BuildSeed waits for the
// default manifest bundle to settle before giving up, so a broken
// throwaway cluster fails the build fast instead of hanging forever.
const buildSeedManifestSettleTimeout = 60 * time.Second

// BuildSeed boots a throwaway cluster named buildName (not seeded itself —
// a seed can't bootstrap from another seed of unknown provenance), waits
// for its default manifest bundle (CoreDNS, local-path-provisioner) to
// settle, stops it cleanly so kine checkpoints its SQLite WAL, copies its
// datastore file to outPath, then deletes the throwaway cluster's state
// entirely. The caller is responsible for choosing a buildName that won't
// collide with a real cluster.
//
// If anything after Create succeeds subsequently fails, BuildSeed
// best-effort stops and deletes the throwaway cluster before returning, so
// a failed build never leaves it running or lingering on disk — mirroring
// Start's own failure-cleanup contract.
func (r *Runtime) BuildSeed(ctx context.Context, buildName, outPath string) (err error) {
	if err = r.Create(ctx, buildName, substrate.StartOptions{}); err != nil {
		return fmt.Errorf("hostproc: seed build: %w", err)
	}

	defer func() {
		if err != nil {
			_ = r.Stop(context.Background(), buildName)
			_ = r.Delete(context.Background(), buildName)
			_ = os.RemoveAll(cluster.Dir(r.homeDir, buildName))
		}
	}()

	if err = r.Start(ctx, buildName, substrate.StartOptions{}); err != nil {
		return fmt.Errorf("hostproc: seed build: %w", err)
	}

	if err = waitDefaultManifestsSettled(ctx, r.kubeconfigPath(buildName)); err != nil {
		return fmt.Errorf("hostproc: seed build: waiting for default manifests to settle: %w", err)
	}

	// Stop (not Delete yet): kine only checkpoints its SQLite WAL into
	// SeedSourcePath on a clean SIGTERM, so the datastore file must be
	// captured after Stop, before Delete removes it along with everything
	// else under the throwaway cluster's data directory.
	if err = r.Stop(context.Background(), buildName); err != nil {
		return fmt.Errorf("hostproc: seed build: stopping throwaway cluster: %w", err)
	}

	if err = os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("hostproc: seed build: creating seed output directory: %w", err)
	}

	if err = copyFile(r.SeedSourcePath(buildName), outPath); err != nil {
		return fmt.Errorf("hostproc: seed build: capturing seed: %w", err)
	}

	if err = r.Delete(context.Background(), buildName); err != nil {
		return fmt.Errorf("hostproc: seed build: deleting throwaway cluster: %w", err)
	}

	// Delete only removes dataDir (PKI, datastore, containerd/kubelet
	// state — see its doc comment); the cluster's top-level directory
	// (kubeconfig, state.json) is a separate cleanup step, same as
	// cmd/rask/delete.go's deleteCluster does after calling Runtime.Delete.
	if err = os.RemoveAll(cluster.Dir(r.homeDir, buildName)); err != nil {
		return fmt.Errorf("hostproc: seed build: removing throwaway cluster directory: %w", err)
	}

	return nil
}

// waitDefaultManifestsSettled waits for both Deployments applyManifests
// creates (CoreDNS and local-path-provisioner) to report a Ready replica,
// in parallel since neither depends on the other.
func waitDefaultManifestsSettled(ctx context.Context, kubeconfigPath string) error {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, buildSeedManifestSettleTimeout)
	defer cancel()

	g, gctx := errgroup.WithContext(waitCtx)

	g.Go(func() error { return manifests.WaitDeploymentReady(gctx, clientset, "kube-system", "coredns") })
	g.Go(func() error {
		return manifests.WaitDeploymentReady(gctx, clientset, "local-path-storage", "local-path-provisioner")
	})

	return g.Wait()
}
