package components

import (
	"fmt"
	"runtime"
)

// Pinned third-party component versions. These mirror the versions proven
// working in spikes/s1/fetch.sh. Only the Kubernetes version is expected to
// vary across rask releases (via Ensure's k8sVersion parameter); the rest
// are pinned here because rask's boot DAG and generated configs are tested
// against these exact releases.
const (
	KineVersion       = "v0.16.3"
	ContainerdVersion = "2.3.3"
	RuncVersion       = "v1.5.1"
	CNIPluginsVersion = "v1.9.1"

	// DefaultK8sVersion is used when a cluster does not request a
	// specific Kubernetes version.
	DefaultK8sVersion = "v1.33.13"
)

// Arch is a Kubernetes/Go-style architecture identifier, restricted to the
// two rask supports.
type Arch string

const (
	AMD64 Arch = "amd64"
	ARM64 Arch = "arm64"
)

// HostArch returns the Arch matching the running process's GOARCH.
func HostArch() (Arch, error) {
	switch runtime.GOARCH {
	case "amd64":
		return AMD64, nil
	case "arm64":
		return ARM64, nil
	default:
		return "", fmt.Errorf("components: unsupported architecture %q (rask supports amd64 and arm64)", runtime.GOARCH)
	}
}

// Paths is the set of resolved, absolute paths to every binary and
// directory rask's boot DAG needs, for one (Kubernetes version, arch) pair.
type Paths struct {
	KubeAPIServer         string
	KubeControllerManager string
	KubeScheduler         string
	Kubelet               string
	KubeProxy             string
	Kubectl               string
	Kine                  string
	// Containerd is the path to the containerd binary. It must be
	// invoked from ContainerdBinDir (its own directory) unmoved:
	// containerd's runtime v2 shim resolution checks the directory
	// containing its own binary before falling back to PATH, and that
	// directory also holds containerd-shim-runc-v2.
	Containerd       string
	ContainerdBinDir string
	Runc             string
	// CNIBinDir contains the bridge/host-local/loopback/portmap plugin
	// binaries (and others rask does not use).
	CNIBinDir string
}
