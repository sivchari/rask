//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/bootstrap"
	"github.com/sivchari/rask/internal/guestconfig"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/store/kine"
)

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
//
// ctx is passed straight through to bootstrap.Boot as its launchCtx —
// deliberately NOT wrapped in a context.WithTimeout here, even though an
// unbounded boot would otherwise leave PID 1 blocked forever if a
// component never becomes healthy. bootstrap.Boot's own doc comment is
// explicit about why: every long-running process it launches
// (kube-apiserver included) is tied to launchCtx via exec.CommandContext,
// so canceling it — including via a deferred cancel() that fires the
// moment this function RETURNS SUCCESSFULLY — kills every one of them
// instantly. This is not hypothetical: an earlier version of this function
// did exactly that (wrapped ctx in a 3-minute timeout with `defer
// cancel()`), and it silently SIGKILLed the entire control plane
// (kube-apiserver's own log just stopped mid-line, no shutdown message)
// the instant boot succeeded — found live, the exact bug class already
// documented from an earlier hostproc boot-timeout incident
// ("errgroup.WithContext's derived context is canceled the first time
// Wait returns, INCLUDING a successful return"), reintroduced here via a
// different mechanism (a bare context.WithTimeout instead of an
// errgroup-derived context) with the identical effect. Bounding overall
// boot time is instead the host-side boot watchdog's job
// (internal/substrate/vz/watchdog.go's runBootWatchdog, which polls this
// guest's own HTTP healthz from outside and stops the VM if it never
// answers — it never touches this guest's internal process-lifetime
// context at all, so it can't have this failure mode).
//
// guestCfg is what internal/substrate/vz staged for this cluster via its
// cluster-config overlay (see internal/guestconfig's package doc):
// guestCfg.ExtraAPIServerArgs is passed straight through to
// bootstrap.Config.ExtraAPIServerArgs, and guestCfg.CoreDNSImage overrides
// which CoreDNS image applyManifests applies.
//
// Concurrently with bootstrap.Boot, importPrefetchedImages imports whatever
// internal/substrate/vz's images overlay shipped into
// guestlayout.ImagesDir into this guest's own containerd, so
// applyManifests' CoreDNS/local-path-provisioner pods (and every pod
// sandbox's pause image) find them already present instead of pulling from
// a registry — mirroring internal/substrate/hostproc.Start's identical
// concurrent-with-Boot import. Waited for (not folded into the
// bootstrap.Boot call itself) before applyManifests runs, for the same
// reason hostproc.Start's own imagesDone channel is waited for before its
// own applyManifests call: it must not race writes into
// dataDir/containerd with bootstrap.Boot's own containerd bring-up, but
// applyManifests scheduling CoreDNS's pod before the import finishes would
// defeat the whole optimization.
func runBoot(ctx context.Context, clusterName string, guestCfg guestconfig.Config) (*bootstrap.Result, error) {
	dataDir := guestlayout.GuestAgentDataDir
	paths := guestlayout.Paths()

	datastore := kine.New(paths.Kine, filepath.Join(dataDir, "kine"))

	imagesDone := make(chan struct{})

	go func() {
		defer close(imagesDone)
		importPrefetchedImages(ctx, containerdSocketPath(dataDir))
	}()

	result, err := bootstrap.Boot(ctx, bootstrap.Config{
		ClusterName:        clusterName,
		DataDir:            dataDir,
		NodeIP:             guestIP,
		Paths:              paths,
		Datastore:          datastore,
		ExtraAPIServerArgs: guestCfg.ExtraAPIServerArgs,
	})

	<-imagesDone

	if err != nil {
		dumpComponentLogs(dataDir)

		return nil, fmt.Errorf("rask-init: %w", err)
	}

	if err := applyManifests(ctx, result.AdminKubeconfigPath, coreDNSImageOrDefault(guestCfg.CoreDNSImage)); err != nil {
		result.Supervisor.Stop()
		_ = datastore.Stop(context.Background())
		dumpComponentLogs(dataDir)

		return nil, fmt.Errorf("rask-init: %w", err)
	}

	return result, nil
}

// containerdSocketPath is the socket internal/bootstrap/config.go's
// writeContainerdConfig configures this cluster's containerd instance to
// listen on, reconstructed the same way
// internal/substrate/hostproc.Runtime.containerdSocketPath does (bootstrap
// exposes no accessor for it — both substrates independently derive it
// from the same dataDir/"containerd"/"containerd.sock" layout
// writeContainerdConfig actually writes).
func containerdSocketPath(dataDir string) string {
	return filepath.Join(dataDir, "containerd", "containerd.sock")
}

// coreDNSImageOrDefault returns img if set, else manifests.CoreDNSImage —
// the same zero-value convention every other CoreDNSImage override in this
// codebase uses (substrate.StartOptions.CoreDNSImage,
// internal/guestconfig.Config.CoreDNSImage,
// internal/substrate/hostproc's own coreDNSImage helper).
func coreDNSImageOrDefault(img string) string {
	if img != "" {
		return img
	}

	return manifests.CoreDNSImage
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
func applyManifests(ctx context.Context, kubeconfigPath, coreDNSImage string) error {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("building rest.Config: %w", err)
	}

	clientset, dyn, mapper, err := manifests.BuildClients(restConfig)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error { return manifests.ApplyCoreDNS(gctx, clientset, coreDNSImage) })
	g.Go(func() error { return manifests.ApplyLocalPathProvisioner(gctx, dyn, mapper) })

	if err := g.Wait(); err != nil {
		return fmt.Errorf("applying manifests: %w", err)
	}

	return nil
}
