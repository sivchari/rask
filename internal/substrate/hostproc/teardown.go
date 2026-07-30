//go:build linux

package hostproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// stopGracePeriod is how long Stop waits after SIGTERM before escalating to
// SIGKILL, mirroring the grace period spikes/s1 used for a clean teardown
// (letting kine/etc. checkpoint state).
const stopGracePeriod = 300 * time.Millisecond

// runningMarkerName is created by Start on success and removed by Stop,
// so Delete can tell whether the cluster is still running (Delete on a
// running cluster is an error per substrate.Runtime's contract).
const runningMarkerName = "RUNNING"

func (r *Runtime) statePath(name string) string {
	return filepath.Join(r.dataDir(name), "state.json")
}

func (r *Runtime) runningMarkerPath(name string) string {
	return filepath.Join(r.dataDir(name), runningMarkerName)
}

// Stop terminates every process a prior Start launched for name (read back
// from the state file it persisted, since Stop typically runs in a
// different CLI invocation than Start — see package doc) and cleans up
// host-level state a hard process kill can leave behind: containerd's
// overlayfs mounts under dataDir, and the CNI bridge. It does not delete
// the cluster's data directory, so a resumed Start could reuse it.
func (r *Runtime) Stop(_ context.Context, name string) error {
	statePath := r.statePath(name)

	state, err := readState(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Nothing was ever started (or it was already stopped);
			// treat as a no-op rather than an error, matching
			// bootstrap.Supervisor.Stop's "safe to call more than
			// once" contract.
			return nil
		}

		return err
	}

	pids := make([]int, 0, len(state.ProcessPIDs)+1)
	for _, pid := range state.ProcessPIDs {
		pids = append(pids, pid)
	}

	if state.DatastorePID != 0 {
		pids = append(pids, state.DatastorePID)
	}

	killAll(pids, syscall.SIGTERM)
	time.Sleep(stopGracePeriod)
	killAll(pids, syscall.SIGKILL)

	unmountUnder(r.dataDir(name))
	removeCNIBridge()

	if err := os.Remove(r.runningMarkerPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hostproc: removing running marker: %w", err)
	}

	return nil
}

// Delete removes the cluster's data directory. It errors if the running
// marker Start created is still present (i.e. Stop has not been called),
// matching substrate.Runtime's documented contract.
func (r *Runtime) Delete(_ context.Context, name string) error {
	if _, err := os.Stat(r.runningMarkerPath(name)); err == nil {
		return fmt.Errorf("hostproc: cluster %q is still running (call Stop first)", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hostproc: checking running marker: %w", err)
	}

	if err := os.RemoveAll(r.dataDir(name)); err != nil {
		return fmt.Errorf("hostproc: removing data dir: %w", err)
	}

	return nil
}

// killAll best-effort signals every pid in pids, skipping ones that are
// already gone.
func killAll(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, sig)
	}
}

// unmountUnder lazily unmounts every mount point nested under dir (e.g.
// overlayfs snapshots containerd leaves behind after creating a pod
// sandbox), deepest-first, so removing dir afterward doesn't fail with
// EBUSY. Best-effort: errors are ignored, matching spikes/s1's teardown.
func unmountUnder(dir string) {
	mounts, err := mountPointsUnder(dir)
	if err != nil {
		return
	}

	sort.Slice(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })

	for _, m := range mounts {
		_ = syscall.Unmount(m, syscall.MNT_DETACH)
	}
}

func mountPointsUnder(dir string) ([]string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	prefix := strings.TrimRight(dir, "/") + "/"

	var mounts []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}

		if strings.HasPrefix(fields[1], prefix) {
			mounts = append(mounts, fields[1])
		}
	}

	return mounts, scanner.Err()
}

// removeCNIBridge removes the host-side "cni0" bridge the bridge CNI
// plugin creates on first pod sandbox, so a later cluster doesn't inherit
// stale bridge/iptables state. Best-effort: absent on a cluster that never
// scheduled a pod.
func removeCNIBridge() {
	_ = exec.Command("ip", "link", "delete", "cni0").Run()
}
