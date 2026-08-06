package components

import (
	"context"
	"fmt"
	"path/filepath"

	"golang.org/x/sync/errgroup"
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
//
// Fetched concurrently, not one at a time: each bin is an independent
// download+checksum-verify round trip against dl.k8s.io, so on a cold
// cache (the only time this does any network I/O at all — see Ensure's
// own doc comment) serializing all of them needlessly multiplies latency
// by len(bins) for no benefit. Found to matter live during this
// investigation: buildTemplateInitramfs's own cold-cache time (this is
// its single largest contributor) measured close enough to vz.go's
// bootTimeout to explain a reported "hung" vz guest boot with no actual
// guest-side bug at all.
func (c *Cache) ensureK8sBinaries(ctx context.Context, k8sVersion string, arch Arch, bins []string) (*Paths, error) {
	subdir := fmt.Sprintf("k8s-%s-%s", k8sVersion, arch)
	results := make([]string, len(bins))

	g, gctx := errgroup.WithContext(ctx)

	for i, bin := range bins {
		g.Go(func() error {
			path, err := c.ensureFile(gctx, subdir, bin, k8sBinaryURL(k8sVersion, arch, bin), k8sChecksumURL(k8sVersion, arch, bin), "")
			if err != nil {
				return fmt.Errorf("components: fetching %s: %w", bin, err)
			}

			results[i] = path

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	paths := &Paths{}

	for i, bin := range bins {
		switch bin {
		case "kube-apiserver":
			paths.KubeAPIServer = results[i]
		case "kube-controller-manager":
			paths.KubeControllerManager = results[i]
		case "kube-scheduler":
			paths.KubeScheduler = results[i]
		case "kubelet":
			paths.Kubelet = results[i]
		case "kube-proxy":
			paths.KubeProxy = results[i]
		case "kubectl":
			paths.Kubectl = results[i]
		default:
			return nil, fmt.Errorf("components: unknown k8s binary %q", bin)
		}
	}

	return paths, nil
}

// ensureNonK8sInto resolves kine, runc, containerd and the CNI plugins —
// every component that is never part of a ComponentSource override — into
// the corresponding fields of paths.
//
// Fetched concurrently: like ensureK8sBinaries, these four are independent
// downloads with no data dependency on one another, so nothing is gained by
// serializing them on a cold cache. Each goroutine below only ever writes
// to fields of paths that no other goroutine touches, and every read of
// paths happens after g.Wait() returns, so this needs no extra
// synchronization beyond errgroup's own.
func (c *Cache) ensureNonK8sInto(ctx context.Context, arch Arch, paths *Paths) (*Paths, error) {
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		kinePath, err := c.ensureFile(gctx, fmt.Sprintf("kine-%s-%s", KineVersion, arch), "kine", kineURL(arch), kineChecksumURL(arch), kineFilename(arch))
		if err != nil {
			return fmt.Errorf("components: fetching kine: %w", err)
		}

		paths.Kine = kinePath

		return nil
	})

	g.Go(func() error {
		runcPath, err := c.ensureFile(gctx, fmt.Sprintf("runc-%s-%s", RuncVersion, arch), "runc", runcURL(arch), runcChecksumURL(), runcFilename(arch))
		if err != nil {
			return fmt.Errorf("components: fetching runc: %w", err)
		}

		paths.Runc = runcPath

		return nil
	})

	g.Go(func() error {
		containerdDir, err := c.ensureArchive(gctx, fmt.Sprintf("containerd-%s-%s", ContainerdVersion, arch), containerdArchive(arch), containerdURL(arch), containerdChecksumURL(arch), containerdArchive(arch))
		if err != nil {
			return fmt.Errorf("components: fetching containerd: %w", err)
		}

		paths.ContainerdBinDir = filepath.Join(containerdDir, "bin")
		paths.Containerd = filepath.Join(paths.ContainerdBinDir, "containerd")

		return nil
	})

	g.Go(func() error {
		cniDir, err := c.ensureArchive(gctx, fmt.Sprintf("cni-plugins-%s-%s", CNIPluginsVersion, arch), cniArchive(arch), cniURL(arch), cniChecksumURL(arch), cniArchive(arch))
		if err != nil {
			return fmt.Errorf("components: fetching cni-plugins: %w", err)
		}

		paths.CNIBinDir = cniDir

		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return paths, nil
}
