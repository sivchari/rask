//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/bootstrap"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/store/kine"
)

// bootTimeout bounds runBoot: a component that never becomes healthy would
// otherwise leave PID 1 blocked forever (internal/bootstrap.Boot's readiness
// polling has no built-in deadline of its own — see runBoot's ctx). Chosen
// generously above what a healthy boot needs, so this only ever fires on a
// genuine stuck-component failure, not a slow-but-working one.
const bootTimeout = 3 * time.Minute

// runBoot brings up the cluster's control plane and node inside this guest,
// reusing internal/bootstrap.Boot unchanged — the same DAG
// internal/substrate/hostproc runs directly on a Linux host. All of
// bootstrap's assumptions (component binaries at fixed paths, a native
// filesystem DataDir, a store.Datastore to inject) hold here too: binaries
// come from guestlayout.Paths() instead of internal/components.Cache.Ensure
// (baked into the initramfs at create time, not downloaded at boot), and
// DataDir is the just-mounted ext4 data disk, not tmpfs.
//
// Once the node is Ready, it also applies CoreDNS and local-path-provisioner
// (internal/manifests), mirroring internal/substrate/hostproc.applyManifests:
// bootstrap.Boot itself only brings up the control plane and node, not
// cluster addons.
func runBoot(ctx context.Context, clusterName string) (*bootstrap.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()

	dataDir := guestlayout.GuestAgentDataDir
	paths := guestlayout.Paths()

	datastore := kine.New(paths.Kine, filepath.Join(dataDir, "kine"))

	result, err := bootstrap.Boot(ctx, bootstrap.Config{
		ClusterName: clusterName,
		DataDir:     dataDir,
		NodeIP:      guestIP,
		Paths:       paths,
		Datastore:   datastore,
	})
	if err != nil {
		dumpComponentLogs(dataDir)

		return nil, fmt.Errorf("rask-init: %w", err)
	}

	if err := applyManifests(ctx, result.AdminKubeconfigPath); err != nil {
		result.Supervisor.Stop()
		_ = datastore.Stop(context.Background())
		dumpComponentLogs(dataDir)

		return nil, fmt.Errorf("rask-init: %w", err)
	}

	return result, nil
}

// dumpComponentLogs prints the tail of every component's log file
// (internal/bootstrap.ProcessSpec.LogPath, under dataDir/logs) to the
// console on a boot failure — otherwise those logs are stranded on the
// guest's data disk with no way for the host to see them (there is no
// shell in this guest to inspect it after the fact), making a stuck-or-
// crash-looping component impossible to diagnose from the host side.
func dumpComponentLogs(dataDir string) {
	logDir := filepath.Join(dataDir, "logs")

	entries, err := os.ReadDir(logDir)
	if err != nil {
		fmt.Printf("RASK-INIT-LOG-DUMP-FAILED reading %s: %v\n", logDir, err)

		return
	}

	const tailBytes = 4096

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		path := filepath.Join(logDir, e.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("RASK-INIT-LOG-DUMP %s: read error: %v\n", e.Name(), err)

			continue
		}

		totalSize := len(data)
		if len(data) > tailBytes {
			data = data[len(data)-tailBytes:]
		}

		fmt.Printf("RASK-INIT-LOG-DUMP-BEGIN %s (%d bytes total, showing last %d)\n%s\nRASK-INIT-LOG-DUMP-END %s\n",
			e.Name(), totalSize, len(data), data, e.Name())
	}
}

// applyManifests applies CoreDNS and local-path-provisioner (+ default
// StorageClass) to the cluster reachable via kubeconfigPath, in parallel
// since neither depends on the other. Identical in shape to
// internal/substrate/hostproc's applyManifests; not shared as a common
// helper since the two substrates' surrounding error-handling/cleanup
// differs enough (hostproc is a separate CLI process; this runs inline in
// rask-init's own boot sequence) that a shared function would need extra
// parameters for no real duplication savings.
func applyManifests(ctx context.Context, kubeconfigPath string) error {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("building rest.Config: %w", err)
	}

	clientset, dyn, mapper, err := manifests.BuildClients(restConfig)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error { return manifests.ApplyCoreDNS(gctx, clientset) })
	g.Go(func() error { return manifests.ApplyLocalPathProvisioner(gctx, dyn, mapper) })

	if err := g.Wait(); err != nil {
		return fmt.Errorf("applying manifests: %w", err)
	}

	return nil
}
