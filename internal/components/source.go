package components

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ComponentSource resolves the complete set of component binary Paths a
// rask cluster's boot DAG needs, for one (k8sVersion, arch) pair. The
// default (DownloadCacheSource) downloads and verifies every component from
// its pinned upstream release via Cache; LocalDirSource lets a caller that
// has already resolved and extracted its own core Kubernetes binaries
// (e.g. fjord, from an EKS-D kubernetes-server tarball) substitute them in
// directly, skipping rask's own dl.k8s.io download entirely.
type ComponentSource interface {
	// Resolve returns the resolved Paths for k8sVersion and arch, or an
	// error if any required binary cannot be found/verified.
	Resolve(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error)
}

// localDirK8sBinaries are the core Kubernetes binaries LocalDirSource
// expects to find, by filename, directly under its directory.
//
// kube-proxy is deliberately excluded: rask always runs it as a supervised
// host process built from the default download cache (see
// internal/bootstrap/boot.go's bootKubeProxy), regardless of a
// --component-dir override. EKS runs kube-proxy as a containerized addon
// rather than a host binary extracted from the kubernetes-server tarball,
// so fjord (this abstraction's motivating caller) has no EKS-D-derived
// kube-proxy binary to hand rask in the first place.
var localDirK8sBinaries = []string{
	"kube-apiserver",
	"kube-controller-manager",
	"kube-scheduler",
	"kubelet",
	"kubectl",
}

// DownloadCacheSource is the default ComponentSource: every component comes
// from cache's versioned download cache (internal/components.Cache.Ensure).
type DownloadCacheSource struct {
	cache *Cache
}

// NewDownloadCacheSource returns a ComponentSource backed by cache.
func NewDownloadCacheSource(cache *Cache) *DownloadCacheSource {
	return &DownloadCacheSource{cache: cache}
}

// Resolve implements ComponentSource.
func (s *DownloadCacheSource) Resolve(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error) {
	return s.cache.Ensure(ctx, k8sVersion, arch)
}

// LocalDirSource is a ComponentSource that substitutes pre-extracted
// kube-apiserver/kube-controller-manager/kube-scheduler/kubelet/kubectl
// binaries from dir, validated present and executable up front so a
// misconfigured --component-dir fails fast with a clear error instead of a
// confusing exec failure deep in the boot DAG. Every other component
// (kube-proxy, kine, containerd, runc, the CNI plugins) still resolves from
// the wrapped Cache, matching the ComponentSource interface's doc comment.
type LocalDirSource struct {
	dir   string
	cache *Cache
}

// NewLocalDirSource returns a ComponentSource that reads
// kube-apiserver/kube-controller-manager/kube-scheduler/kubelet/kubectl
// from dir, falling back to cache (the default download cache) for every
// other component.
func NewLocalDirSource(dir string, cache *Cache) *LocalDirSource {
	return &LocalDirSource{dir: dir, cache: cache}
}

// Resolve implements ComponentSource: it validates dir up front, then
// resolves kube-proxy plus every non-Kubernetes component from the wrapped
// Cache, and finally overlays the five local binary paths.
func (s *LocalDirSource) Resolve(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}

	paths, err := s.cache.ensureNonOverridden(ctx, k8sVersion, arch)
	if err != nil {
		return nil, err
	}

	paths.KubeAPIServer = filepath.Join(s.dir, "kube-apiserver")
	paths.KubeControllerManager = filepath.Join(s.dir, "kube-controller-manager")
	paths.KubeScheduler = filepath.Join(s.dir, "kube-scheduler")
	paths.Kubelet = filepath.Join(s.dir, "kubelet")
	paths.Kubectl = filepath.Join(s.dir, "kubectl")

	return paths, nil
}

// validate checks that every binary LocalDirSource overrides is present
// under s.dir and executable, returning a single error naming every problem
// found rather than failing on the first one, so a caller fixing up a
// component-dir doesn't have to run rask create repeatedly to discover each
// missing binary one at a time.
func (s *LocalDirSource) validate() error {
	var problems []error

	for _, bin := range localDirK8sBinaries {
		path := filepath.Join(s.dir, bin)

		info, err := os.Stat(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))

			continue
		}

		if info.IsDir() {
			problems = append(problems, fmt.Errorf("%s: is a directory, want a regular file", path))

			continue
		}

		if info.Mode().Perm()&0o111 == 0 {
			problems = append(problems, fmt.Errorf("%s: is not executable", path))
		}
	}

	if len(problems) == 0 {
		return nil
	}

	err := fmt.Errorf("components: component-dir %s is missing or has invalid binaries", s.dir)
	for _, p := range problems {
		err = fmt.Errorf("%w\n  - %w", err, p)
	}

	return err
}
