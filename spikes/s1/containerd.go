package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// containerdConfigTemplate targets containerd 2.3.x's split CRI plugin
// schema (io.containerd.cri.v1.images / io.containerd.cri.v1.runtime).
// Only fields that must diverge from containerd's built-in defaults are
// set; everything else is left to the plugin registration defaults.
const containerdConfigTemplate = `version = 4
root = %q
state = %q
imports = []

[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'overlayfs'

  [plugins.'io.containerd.cri.v1.images'.pinned_images]
    sandbox = %q

[plugins.'io.containerd.cri.v1.runtime']
  [plugins.'io.containerd.cri.v1.runtime'.containerd]
    default_runtime_name = 'runc'

    [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
      runtime_type = 'io.containerd.runc.v2'

      [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]
        BinaryName = %q
        SystemdCgroup = false

  [plugins.'io.containerd.cri.v1.runtime'.cni]
    bin_dir = %q
    conf_dir = %q
    max_conf_num = 1

[plugins.'io.containerd.server.v1.grpc']
  address = %q
  uid = 0
  gid = 0
`

// sandboxImage is the CRI pause image, pinned to match kubelet 1.33's
// expected pause version.
const sandboxImage = "registry.k8s.io/pause:3.10"

type containerdPaths struct {
	configPath string
	socketPath string
	root       string
	state      string
	cniConfDir string
}

// writeContainerdConfig renders config.toml for a standalone containerd
// instance scoped entirely to datadir, so it never touches colima's own
// docker/containerd state.
func writeContainerdConfig(datadir, runcPath, cniBinDir string) (*containerdPaths, error) {
	base := filepath.Join(datadir, "containerd")
	paths := &containerdPaths{
		configPath: filepath.Join(base, "config.toml"),
		socketPath: filepath.Join(base, "containerd.sock"),
		root:       filepath.Join(base, "root"),
		state:      filepath.Join(base, "state"),
		cniConfDir: filepath.Join(datadir, "cni", "net.d"),
	}

	for _, dir := range []string{base, paths.root, paths.state} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating containerd dir %s: %w", dir, err)
		}
	}

	content := fmt.Sprintf(containerdConfigTemplate,
		paths.root,
		paths.state,
		sandboxImage,
		runcPath,
		cniBinDir,
		paths.cniConfDir,
		paths.socketPath,
	)
	if err := os.WriteFile(paths.configPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("writing containerd config: %w", err)
	}

	return paths, nil
}
