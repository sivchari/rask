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
func (c *Cache) Ensure(ctx context.Context, k8sVersion string, arch Arch) (*Paths, error) {
	paths := &Paths{}

	k8sBinPaths := make(map[string]string, len(k8sBinaries))

	for _, bin := range k8sBinaries {
		subdir := fmt.Sprintf("k8s-%s-%s", k8sVersion, arch)

		path, err := c.ensureFile(ctx, subdir, bin, k8sBinaryURL(k8sVersion, arch, bin), k8sChecksumURL(k8sVersion, arch, bin), "")
		if err != nil {
			return nil, fmt.Errorf("components: fetching %s: %w", bin, err)
		}

		k8sBinPaths[bin] = path
	}

	paths.KubeAPIServer = k8sBinPaths["kube-apiserver"]
	paths.KubeControllerManager = k8sBinPaths["kube-controller-manager"]
	paths.KubeScheduler = k8sBinPaths["kube-scheduler"]
	paths.Kubelet = k8sBinPaths["kubelet"]
	paths.KubeProxy = k8sBinPaths["kube-proxy"]
	paths.Kubectl = k8sBinPaths["kubectl"]

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
