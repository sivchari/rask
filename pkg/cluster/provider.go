package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	internalcluster "github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/prebake"
	"github.com/sivchari/rask/internal/substrate"
)

// coreDNSWaitTimeout bounds Options.Wait == WaitCoreDNS so a broken cluster
// fails Create fast instead of hanging forever. Matches
// cmd/rask/create.go's coreDNSWaitTimeout.
const coreDNSWaitTimeout = 60 * time.Second

// Provider creates and manages rask clusters. The zero value is not usable;
// construct one with NewProvider.
//
// A Provider is not safe for concurrent Create/Delete calls against the
// same cluster name (matches internal/substrate.Runtime's own contract);
// concurrent calls against distinct names are fine.
type Provider struct {
	rt      substrate.Runtime
	homeDir string
}

// NewProvider returns a Provider storing cluster state under homeDir. If
// homeDir is empty, the default (~/.rask, matching the rask CLI) is used.
func NewProvider(homeDir string) (*Provider, error) {
	if homeDir == "" {
		hd, err := internalcluster.DefaultHomeDir()
		if err != nil {
			return nil, err
		}

		homeDir = hd
	}

	return &Provider{rt: newPlatformRuntime(homeDir), homeDir: homeDir}, nil
}

// NewProviderWithRuntime returns a Provider backed by rt directly, bypassing
// platform runtime selection.
//
// Exported so other packages within this module — namely cmd/rask, which
// needs to inject a test double in place of a real substrate.Runtime — can
// reuse Provider's Create/Delete/List/LoadImages logic instead of
// duplicating it. rt's type (internal/substrate.Runtime) cannot be
// constructed or even named from outside this module, so despite being
// exported, this constructor is not actually callable by an external
// importer; real library consumers should use NewProvider instead.
func NewProviderWithRuntime(rt substrate.Runtime, homeDir string) *Provider {
	return &Provider{rt: rt, homeDir: homeDir}
}

// Create creates and starts a new cluster named name, blocking until
// opts.Wait's condition is satisfied (WaitNode, the default if empty, is
// satisfied internally by the time the node is Ready; WaitCoreDNS
// additionally waits for CoreDNS).
func (p *Provider) Create(ctx context.Context, name string, opts Options) (Result, error) {
	wait := opts.Wait
	if wait == "" {
		wait = WaitNode
	}

	if wait != WaitNode && wait != WaitCoreDNS {
		return Result{}, fmt.Errorf("invalid wait %q: must be %q or %q", wait, WaitNode, WaitCoreDNS)
	}

	exists, err := internalcluster.Exists(p.homeDir, name)
	if err != nil {
		return Result{}, err
	}

	if exists {
		return Result{}, fmt.Errorf("cluster %q already exists", name)
	}

	coreDNSImage := opts.CoreDNSImage
	if coreDNSImage == "" {
		coreDNSImage = manifests.CoreDNSImage
	}

	startOpts := substrate.StartOptions{
		ExtraAPIAudiences:  opts.ExtraAPIAudiences,
		ExtraAPIServerArgs: opts.ExtraAPIServerArgs,
		PrebootFiles:       toSubstratePrebootFiles(opts.PrebootFiles),
		ComponentDir:       opts.ComponentDir,
		CoreDNSImage:       opts.CoreDNSImage,
	}

	if err := p.rt.Create(ctx, name, startOpts); err != nil {
		return Result{}, fmt.Errorf("cluster %q: %w", name, err)
	}

	// A seed matching the exact Kubernetes version, default manifest
	// bundle and CoreDNS image this request uses (internal/prebake.Key)
	// is used automatically when present: a pure optimization a substrate
	// implementation applies before booting, never a behavior change.
	if seedPath := prebake.Path(p.homeDir, components.DefaultK8sVersion, coreDNSImage); fileExists(seedPath) {
		startOpts.SeedPath = seedPath
	}

	if err := p.rt.Start(ctx, name, startOpts); err != nil {
		return Result{}, fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := os.MkdirAll(internalcluster.Dir(p.homeDir, name), 0o755); err != nil {
		return Result{}, fmt.Errorf("creating state directory for cluster %q: %w", name, err)
	}

	if wait == WaitCoreDNS {
		if err := p.waitForCoreDNS(ctx, name); err != nil {
			return Result{}, fmt.Errorf("cluster %q: waiting for CoreDNS: %w", name, err)
		}
	}

	return Result{
		KubeconfigPath: p.KubeConfigPath(name),
		Timeline:       p.readTimeline(name),
	}, nil
}

// Delete stops and removes the cluster named name, including its local
// state directory. Errors if the cluster does not exist.
func (p *Provider) Delete(ctx context.Context, name string) error {
	exists, err := internalcluster.Exists(p.homeDir, name)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("cluster %q does not exist", name)
	}

	if err := p.rt.Stop(ctx, name); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := p.rt.Delete(ctx, name); err != nil {
		return fmt.Errorf("cluster %q: %w", name, err)
	}

	if err := os.RemoveAll(internalcluster.Dir(p.homeDir, name)); err != nil {
		return fmt.Errorf("removing state directory for cluster %q: %w", name, err)
	}

	return nil
}

// List returns the names of every cluster with state under this Provider's
// home directory, sorted alphabetically.
func (p *Provider) List() ([]string, error) {
	return internalcluster.List(p.homeDir)
}

// KubeConfigPath returns the path name's admin kubeconfig is (or would be)
// written to.
func (p *Provider) KubeConfigPath(name string) string {
	return filepath.Join(internalcluster.Dir(p.homeDir, name), "kubeconfig")
}

// KubeConfig returns name's admin kubeconfig content, with its
// cluster/user/context name rendered per opts.ContextFormat. Errors if the
// cluster does not exist.
func (p *Provider) KubeConfig(name string, opts ExportOptions) ([]byte, error) {
	exists, err := internalcluster.Exists(p.homeDir, name)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, fmt.Errorf("cluster %q does not exist", name)
	}

	cfg, err := clientcmd.LoadFromFile(p.KubeConfigPath(name))
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig for cluster %q: %w", name, err)
	}

	contextFormat := opts.ContextFormat
	if contextFormat == "" {
		contextFormat = "{name}"
	}

	contextName := internalcluster.FormatContext(contextFormat, name)
	if contextName != name {
		c, ok := cfg.Contexts[name]
		if !ok {
			return nil, fmt.Errorf("kubeconfig for cluster %q has no context named %q", name, name)
		}

		cfg.Contexts[contextName] = c
		delete(cfg.Contexts, name)

		if cfg.CurrentContext == name {
			cfg.CurrentContext = contextName
		}
	}

	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("rendering kubeconfig for cluster %q: %w", name, err)
	}

	return data, nil
}

// LoadImages imports every image in images into name's image store, so a
// pod can reference them with imagePullPolicy: Never without the cluster
// ever needing registry access. Errors if the cluster does not exist.
func (p *Provider) LoadImages(ctx context.Context, name string, images []ImageSource) error {
	exists, err := internalcluster.Exists(p.homeDir, name)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("cluster %q does not exist", name)
	}

	substrateImages := make([]substrate.ImageSource, len(images))
	for i, img := range images {
		substrateImages[i] = substrate.ImageSource{Reference: img.Reference, Stream: img.Stream}
	}

	return p.rt.LoadImages(ctx, name, substrateImages)
}

// waitForCoreDNS polls the CoreDNS Deployment (applied by internal/manifests
// during Create) until it reports at least one Ready replica.
func (p *Provider) waitForCoreDNS(ctx context.Context, name string) error {
	restConfig, err := clientcmd.BuildConfigFromFlags("", p.KubeConfigPath(name))
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

// readTimeline best-effort reads the phase-by-phase boot latency breakdown
// a substrate implementation may have written (see
// internal/substrate/hostproc's timelinePath). Returns "" if absent, since
// not every substrate necessarily produces one.
func (p *Provider) readTimeline(name string) string {
	path := filepath.Join(internalcluster.Dir(p.homeDir, name), "data", "timeline.txt")

	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return string(data)
}

// toSubstratePrebootFiles converts the public PrebootFile slice into
// internal/substrate's equivalent, keeping that internal type out of this
// package's own exported API.
func toSubstratePrebootFiles(files []PrebootFile) []substrate.PrebootFile {
	if len(files) == 0 {
		return nil
	}

	out := make([]substrate.PrebootFile, len(files))
	for i, f := range files {
		out[i] = substrate.PrebootFile{Src: f.Src, Dest: f.Dest}
	}

	return out
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}
