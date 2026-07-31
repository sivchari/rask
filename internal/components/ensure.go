package components

import (
	"context"
	"fmt"
	"path/filepath"
)

// k8sBinaries is the set of dl.k8s.io release binaries rask needs.
var k8sBinaries = []string{
	"kube-apiserver",
	"kube-controller-manager",
	"kube-scheduler",
	"kubelet",
	"kube-proxy",
	"kubectl",
}

// Ensure downloads (if not already cached), verifies and returns the
// resolved paths of every binary rask's boot DAG needs for k8sVersion and
// arch. Cached components are reused across clusters and across "rask
// create" invocations; only the first call for a given (k8sVersion, arch)
// pair touches the network.
//
// Ensure is also DownloadCacheSource's ComponentSource implementation; see
// ensureK8sBinaries/ensureNonOverridden for the split this and
// LocalDirSource.Resolve both build on.
func (c *Cache) Ensure(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error) {
	paths, err := c.ensureK8sBinaries(ctx, k8sVersion, arch, k8sBinaries)
	if err != nil {
		return nil, err
	}

	return c.ensureNonK8sInto(ctx, arch, paths)
}

// ensureNonOverridden resolves kube-proxy (the one k8sBinaries entry a
// component-dir override never provides — see LocalDirSource's doc comment)
// plus every non-Kubernetes component. Used directly by LocalDirSource.Resolve
// to fill in everything it doesn't overlay with local binary paths.
func (c *Cache) ensureNonOverridden(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error) {
	paths, err := c.ensureK8sBinaries(ctx, k8sVersion, arch, []string{"kube-proxy"})
	if err != nil {
		return nil, err
	}

	return c.ensureNonK8sInto(ctx, arch, paths)
}

// ensureK8sBinaries downloads (if not already cached) and verifies exactly
// the dl.k8s.io release binaries named in bins (a subset of k8sBinaries),
// returning a *Paths with only those fields populated.
func (c *Cache) ensureK8sBinaries(ctx context.Context, k8sVersion string, arch Arch, bins []string) (*Paths, error) {
	paths := &Paths{}

	for _, bin := range bins {
		subdir := fmt.Sprintf("k8s-%s-%s", k8sVersion, arch)

		path, err := c.ensureFile(ctx, subdir, bin, k8sBinaryURL(k8sVersion, arch, bin), k8sChecksumURL(k8sVersion, arch, bin), "")
		if err != nil {
			return nil, fmt.Errorf("components: fetching %s: %w", bin, err)
		}

		switch bin {
		case "kube-apiserver":
			paths.KubeAPIServer = path
		case "kube-controller-manager":
			paths.KubeControllerManager = path
		case "kube-scheduler":
			paths.KubeScheduler = path
		case "kubelet":
			paths.Kubelet = path
		case "kube-proxy":
			paths.KubeProxy = path
		case "kubectl":
			paths.Kubectl = path
		default:
			return nil, fmt.Errorf("components: unknown k8s binary %q", bin)
		}
	}

	return paths, nil
}

// ensureNonK8sInto resolves kine, runc, containerd and the CNI plugins —
// every component that is never part of a ComponentSource override — into
// the corresponding fields of paths.
func (c *Cache) ensureNonK8sInto(ctx context.Context, arch Arch, paths *Paths) (*Paths, error) {
	kinePath, err := c.ensureFile(ctx, fmt.Sprintf("kine-%s-%s", KineVersion, arch), "kine", kineURL(arch), kineChecksumURL(arch), kineFilename(arch))
	if err != nil {
		return nil, fmt.Errorf("components: fetching kine: %w", err)
	}

	paths.Kine = kinePath

	runcPath, err := c.ensureFile(ctx, fmt.Sprintf("runc-%s-%s", RuncVersion, arch), "runc", runcURL(arch), runcChecksumURL(), runcFilename(arch))
	if err != nil {
		return nil, fmt.Errorf("components: fetching runc: %w", err)
	}

	paths.Runc = runcPath

	containerdDir, err := c.ensureArchive(ctx, fmt.Sprintf("containerd-%s-%s", ContainerdVersion, arch), containerdArchive(arch), containerdURL(arch), containerdChecksumURL(arch), containerdArchive(arch))
	if err != nil {
		return nil, fmt.Errorf("components: fetching containerd: %w", err)
	}

	paths.ContainerdBinDir = filepath.Join(containerdDir, "bin")
	paths.Containerd = filepath.Join(paths.ContainerdBinDir, "containerd")

	cniDir, err := c.ensureArchive(ctx, fmt.Sprintf("cni-plugins-%s-%s", CNIPluginsVersion, arch), cniArchive(arch), cniURL(arch), cniChecksumURL(arch), cniArchive(arch))
	if err != nil {
		return nil, fmt.Errorf("components: fetching cni-plugins: %w", err)
	}

	paths.CNIBinDir = cniDir

	return paths, nil
}
