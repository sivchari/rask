//go:build linux

// Package hostproc implements substrate.Runtime by supervising Kubernetes
// components as plain host processes, rather than inside a VM. It targets
// Linux, where kind/k3d normally run inside a container: rask skips the
// container layer entirely.
//
// v1 does not isolate a cluster in its own network namespace: like k3s, it
// assumes it owns the host's network namespace (CNI bridge, kube-proxy
// iptables rules). Running more than one rask cluster at a time on the same
// host, or alongside other CNI-managed workloads, is not yet supported;
// per-cluster netns isolation is a TODO for a later milestone.
//
// A cluster's control plane and node processes are expected to keep running
// after the "rask create" process that launched them exits (that's the
// whole point — a disposable cluster you can run kubectl against later), so
// a later "rask delete" is a *separate* CLI process with no in-memory
// record of them. Start persists what Stop/Delete need (PIDs) to
// state.json under the cluster's data directory; see teardown.go.
package hostproc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/store/kine"
	"github.com/sivchari/rask/internal/substrate"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/tools/clientcmd"
)

// Runtime implements substrate.Runtime using supervised host processes.
// It carries no in-memory per-cluster state: every method derives
// everything it needs from (homeDir, name), so it works correctly when
// Stop or Delete run in a process that never saw the original Start (see
// package doc).
type Runtime struct {
	homeDir string
}

var _ substrate.Runtime = (*Runtime)(nil)

// New returns a Runtime for the Linux host-process substrate, storing
// every cluster's state under homeDir (see internal/cluster.Dir).
func New(homeDir string) *Runtime {
	return &Runtime{homeDir: homeDir}
}

// dataDir is the internal working directory Boot writes PKI, datastore,
// containerd, kubelet and CNI state into.
func (r *Runtime) dataDir(name string) string {
	return filepath.Join(cluster.Dir(r.homeDir, name), "data")
}

// kubeconfigPath is where Start writes the cluster's admin kubeconfig for
// external use. cmd/rask reads this path directly rather than going
// through substrate.Runtime (which has no accessor for it): hostproc has
// no VM boundary, so there's nothing to copy across, and both sides
// already agree on cluster.Dir via the shared homeDir.
func (r *Runtime) kubeconfigPath(name string) string {
	return filepath.Join(cluster.Dir(r.homeDir, name), "kubeconfig")
}

func (r *Runtime) cacheDir() string {
	return filepath.Join(r.homeDir, "cache")
}

// SeedSourcePath returns the path to the kine SQLite file backing name's
// datastore, for internal/prebake to copy out as a seed snapshot after
// Stop. Stop must be called first (not just Create/Start): kine only
// checkpoints its SQLite WAL into this file on a clean SIGTERM (see
// internal/store/kine.Datastore.Stop), so reading it while kine is still
// running could observe a torn/incomplete database.
func (r *Runtime) SeedSourcePath(name string) string {
	return filepath.Join(r.dataDir(name), "kine", "state.db")
}

// timelinePath is where Start writes a human-readable phase breakdown,
// which "rask create --verbose" reads and prints. A file, like the running
// marker and PID state, because whatever process printed --verbose's
// output is the CLI invocation itself, not something that can hold the
// bootstrap.Timeline in memory across the substrate.Runtime interface
// (Start returns only an error).
func (r *Runtime) timelinePath(name string) string {
	return filepath.Join(r.dataDir(name), "timeline.txt")
}

// Create resolves and caches (downloading if necessary) every component
// binary the cluster's boot DAG needs. It does not start anything and,
// deliberately, does not touch cluster.Dir(homeDir, name) at all: it only
// ever writes into the shared cache directory (homeDir/cache), so a failed
// Create (e.g. no network) leaves zero on-disk trace of the cluster —
// which is what lets cluster.Exists (a directory-existence check) and a
// retried "rask create" behave correctly. Start is what actually creates
// cluster.Dir, and cleans it back up on its own failure (see below).
//
// If opts.ComponentDir is set, Create validates it (via
// components.LocalDirSource) instead of downloading the default
// kube-apiserver/kube-controller-manager/kube-scheduler/kubelet/kubectl:
// the whole point of a component-dir override is to avoid rask's own
// dl.k8s.io dependency, so warming that part of the default cache anyway
// would defeat it (and could hard-fail Create in a network-restricted
// environment even though every binary rask actually needs was already
// provided locally).
func (r *Runtime) Create(ctx context.Context, name string, opts substrate.StartOptions) error {
	arch, err := components.HostArch()
	if err != nil {
		return err
	}

	src := r.componentSource(opts)
	if _, err := src.Resolve(ctx, components.DefaultK8sVersion, arch); err != nil {
		return fmt.Errorf("hostproc: preparing component binaries: %w", err)
	}

	return nil
}

// componentSource returns opts.ComponentDir's components.ComponentSource
// (internal/components.LocalDirSource) if set, else the default download
// cache (internal/components.DownloadCacheSource).
func (r *Runtime) componentSource(opts substrate.StartOptions) components.ComponentSource {
	cache := components.NewCache(r.cacheDir())

	if opts.ComponentDir != "" {
		return components.NewLocalDirSource(opts.ComponentDir, cache)
	}

	return components.NewDownloadCacheSource(cache)
}

// Start boots the cluster's control plane and node (internal/bootstrap.Boot),
// blocking until the node is Ready, then persists what a later Stop/Delete
// needs (see teardown.go) and writes the cluster's kubeconfig.
//
// If anything after Boot succeeds subsequently fails, Start stops
// everything Boot launched and removes cluster.Dir(homeDir, name) entirely
// before returning: without this, a failed Start would leave orphaned
// processes with no persisted PID state to ever kill them, AND leave
// cluster.Dir behind, which would make cluster.Exists report true forever
// after and permanently block every retry.
func (r *Runtime) Start(ctx context.Context, name string, opts substrate.StartOptions) (err error) {
	arch, err := components.HostArch()
	if err != nil {
		return err
	}

	src := r.componentSource(opts)

	paths, err := src.Resolve(ctx, components.DefaultK8sVersion, arch)
	if err != nil {
		return fmt.Errorf("hostproc: resolving component binaries: %w", err)
	}

	nodeIP, err := detectOutboundIP()
	if err != nil {
		return err
	}

	dataDir := r.dataDir(name)

	// Must happen before bootstrap.Boot: every preboot file is meant to
	// be readable by a component Boot launches (e.g. an authentication
	// webhook kubeconfig kube-apiserver reads at startup via an
	// ExtraAPIServerArgs flag), so it must exist on disk before any
	// process starts, not just before this cluster is considered ready.
	if err = substrate.StagePrebootFiles(dataDir, opts.PrebootFiles); err != nil {
		return fmt.Errorf("hostproc: %w", err)
	}

	datastore := kine.New(paths.Kine, filepath.Join(dataDir, "kine"))

	result, err := bootstrap.Boot(ctx, bootstrap.Config{
		ClusterName:        name,
		DataDir:            dataDir,
		NodeIP:             nodeIP,
		Paths:              paths,
		Datastore:          datastore,
		ExtraAPIAudiences:  opts.ExtraAPIAudiences,
		SeedPath:           opts.SeedPath,
		ExtraAPIServerArgs: opts.ExtraAPIServerArgs,
	})
	if err != nil {
		return fmt.Errorf("hostproc: %w", err)
	}

	// From here on, real long-running processes exist; any failure
	// below must stop them (Boot itself doesn't know about the
	// remaining steps, so it can't do this on our behalf) and remove
	// whatever Boot wrote to cluster.Dir, for the reasons in the doc
	// comment above.
	defer func() {
		if err != nil {
			result.Supervisor.Stop()
			_ = datastore.Stop(context.Background())
			_ = os.RemoveAll(cluster.Dir(r.homeDir, name))
		}
	}()

	state := runtimeState{ProcessPIDs: result.Supervisor.PIDs()}
	if pid, ok := datastore.PID(); ok {
		state.DatastorePID = pid
	}

	if err = writeState(r.statePath(name), state); err != nil {
		return err
	}

	if err = copyFile(result.AdminKubeconfigPath, r.kubeconfigPath(name)); err != nil {
		return fmt.Errorf("hostproc: writing kubeconfig: %w", err)
	}

	if err = writeTimeline(r.timelinePath(name), result.Timeline); err != nil {
		return err
	}

	// A seeded datastore already contains the CoreDNS and
	// local-path-provisioner objects (see internal/prebake): re-applying
	// them would be a harmless no-op (ApplyYAML/ApplyCoreDNS both ignore
	// AlreadyExists), but skipping the round trip entirely is exactly the
	// create-time cost seeding exists to shave off.
	if opts.SeedPath == "" {
		if err = applyManifests(ctx, result.AdminKubeconfigPath, coreDNSImage(opts)); err != nil {
			return fmt.Errorf("hostproc: %w", err)
		}
	}

	// Created last, once everything else has succeeded: Delete refuses
	// to run while this marker is present, and Stop removes it, so its
	// presence is the source of truth for "is this cluster running".
	if err = os.WriteFile(r.runningMarkerPath(name), nil, 0o644); err != nil {
		return fmt.Errorf("hostproc: writing running marker: %w", err)
	}

	return nil
}

// coreDNSImage returns opts.CoreDNSImage if set, else manifests.CoreDNSImage.
func coreDNSImage(opts substrate.StartOptions) string {
	if opts.CoreDNSImage != "" {
		return opts.CoreDNSImage
	}

	return manifests.CoreDNSImage
}

// applyManifests applies CoreDNS (using coreDNSImage) and
// local-path-provisioner (+ default StorageClass) to the cluster reachable
// via kubeconfigPath, in parallel since neither depends on the other.
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

func writeTimeline(path string, tl *bootstrap.Timeline) error {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "%-20s %12s %12s\n", "PHASE", "CUMULATIVE", "DELTA")

	for _, e := range tl.Breakdown() {
		fmt.Fprintf(&buf, "%-20s %12s %12s\n", e.Name, e.Elapsed.Round(time.Millisecond), e.SincePrev.Round(time.Millisecond))
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("hostproc: writing timeline file %s: %w", path, err)
	}

	return nil
}

// runtimeState is what Start persists for a later Stop/Delete to load.
type runtimeState struct {
	DatastorePID int            `json:"datastorePID"`
	ProcessPIDs  map[string]int `json:"processPIDs"`
}

func writeState(path string, state runtimeState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("hostproc: encoding state: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("hostproc: writing state file %s: %w", path, err)
	}

	return nil
}

func readState(path string) (runtimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimeState{}, fmt.Errorf("hostproc: reading state file %s: %w", path, err)
	}

	var state runtimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return runtimeState{}, fmt.Errorf("hostproc: decoding state file %s: %w", path, err)
	}

	return state, nil
}

// detectOutboundIP returns the local IP address the kernel would use to
// reach the internet, without sending any packets (UDP "connect" just
// selects a route). Used as the API server advertise address / SAN and as
// kubelet's --node-ip.
func detectOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("hostproc: detecting outbound IP: %w", err)
	}
	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("hostproc: unexpected local addr type %T", conn.LocalAddr())
	}

	return addr.IP.String(), nil
}

// copyFile copies src to dst, creating dst's parent directory if needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dst, err)
	}

	return nil
}
