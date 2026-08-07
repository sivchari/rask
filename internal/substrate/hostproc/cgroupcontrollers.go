//go:build linux

package hostproc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cgroupControllersPath lists the cgroup v2 controllers available to this
// cgroup's children, as seen through this process's own cgroup namespace.
// Inside a container with a private cgroup namespace (the default for both
// Docker and containerd), this is exactly what the container runtime
// delegated to the container, not the host's own root cgroup, so it's
// meaningful to read directly without first working out whether this
// process is even inside a container.
const cgroupControllersPath = "/sys/fs/cgroup/cgroup.controllers"

// requiredCgroupControllers is the minimum cgroup v2 controller set
// kubelet needs to build its /kubepods cgroup tree under rask's kubelet
// config (cgroupDriver: cgroupfs, no CPU/memory manager static policy, no
// --pod-max-pids limit — see bootstrap's kubeletConfigTemplate): memory
// and pids back the eviction manager's memory.available/pid.available
// signals, cpu backs the per-QoS-class cgroup hierarchy itself. io
// (blkio) is deliberately not in this list: nothing rask configures
// depends on it.
var requiredCgroupControllers = []string{"cpu", "memory", "pids"}

// ensureCgroupControllersDelegated guarantees the cgroup v2 controllers
// kubelet needs are delegated to this process's cgroup before bootstrap.Boot
// tries to use them, reading cgroupControllersPath as production does.
//
// Found live: running rask inside a `docker run --privileged` container
// (colima docker, kernel 6.8, cgroup v2), Docker/runc's default delegation
// to a privileged container is only cpuset+cpu+pids — memory (and io) are
// withheld — and because the container's root cgroup already has processes
// in it, kubelet cannot add the missing controllers itself afterward
// ("no internal process constraint"). kubelet then fails to create
// /kubepods with "cannot enter cgroupv2 ... invalid state", an error that
// gives no hint that a missing cgroup controller (rather than, say, a
// kubelet config mistake) is the cause. kind avoids this because its base
// image's systemd (PID 1) moves the root process into its own init.scope
// before kubelet starts, freeing the root cgroup to delegate more
// controllers; rask runs no init system, so nothing does that here.
func ensureCgroupControllersDelegated() error {
	return ensureCgroupControllersDelegatedFile(cgroupControllersPath)
}

// ensureCgroupControllersDelegatedFile is ensureCgroupControllersDelegated
// with the cgroup.controllers path injected so the logic is
// unit-testable.
func ensureCgroupControllersDelegatedFile(path string) error {
	content, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		// No cgroup v2 unified hierarchy at the conventional mount point:
		// either this is a cgroup v1 host (which has no cgroup.controllers
		// file at all — every subsystem is its own separate mount) or
		// cgroup2 simply isn't mounted at /sys/fs/cgroup. Either way,
		// controller delegation as this file would report it doesn't
		// apply, so there's nothing to flag here.
		return nil
	}

	if err != nil {
		return fmt.Errorf("hostproc: reading %s: %w", path, err)
	}

	available := strings.Fields(string(content))

	have := make(map[string]bool, len(available))
	for _, c := range available {
		have[c] = true
	}

	var missing []string

	for _, c := range requiredCgroupControllers {
		if !have[c] {
			missing = append(missing, c)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("hostproc: cgroup v2 controllers %s are not delegated to this process's cgroup (%s lists only %s): kubelet cannot build its /kubepods cgroup tree without them and boot fails with an unrelated-looking \"cannot enter cgroupv2 ... invalid state\" error; if rask is running inside a container, delegate these controllers to it (e.g. Docker: run with --cgroupns=host so the container shares the host's already fully delegated root cgroup instead of a restricted private one) or otherwise ensure the container runtime delegates cpu/memory/pids to the container's cgroup", missing, path, available)
}
