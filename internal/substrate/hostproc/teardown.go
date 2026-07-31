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
	"strconv"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
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
// overlayfs mounts under dataDir, the CNI bridge, and any
// containerd-shim-runc-v2 process left running. It does not delete the
// cluster's data directory, so a resumed Start could reuse it.
//
// Order matters here, and exists to avoid orphaning shim processes: a
// shim is deliberately designed to survive its containerd daemon dying
// (see gracefulStopPods's doc comment), so simply killing containerd —
// gracefully or not — never stops the shims it was supervising. The only
// reliable way found (see test/benchmark/PROGRESS-robustness.md for the
// investigation) is the exact same path every ordinary pod deletion
// already takes: ask the cluster's own API server to delete each pod while
// kubelet and containerd are still alive, and wait for kubelet/CRI to
// actually finish tearing it down. So: stop controller-manager/scheduler
// first (nothing should recreate a pod this is about to delete), delete
// every pod gracefully and wait (bounded), then hard-kill everything else
// as before, then sweep for any shim that's still somehow running.
func (r *Runtime) Stop(ctx context.Context, name string) error {
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

	controlPlanePIDs := lookupPIDs(state.ProcessPIDs, "kube-controller-manager", "kube-scheduler")
	killAll(controlPlanePIDs, syscall.SIGTERM)
	time.Sleep(stopGracePeriod)
	killAll(controlPlanePIDs, syscall.SIGKILL)

	gracefulStopPods(ctx, r.kubeconfigPath(name))

	remainingPIDs := make([]int, 0, len(state.ProcessPIDs)+1)

	for procName, pid := range state.ProcessPIDs {
		if procName == "kube-controller-manager" || procName == "kube-scheduler" {
			continue
		}

		remainingPIDs = append(remainingPIDs, pid)
	}

	if state.DatastorePID != 0 {
		remainingPIDs = append(remainingPIDs, state.DatastorePID)
	}

	killAll(remainingPIDs, syscall.SIGTERM)
	time.Sleep(stopGracePeriod)
	killAll(remainingPIDs, syscall.SIGKILL)

	killOrphanedShims(r.dataDir(name))

	unmountUnder(r.dataDir(name))
	removeCNIBridge()

	if err := os.Remove(r.runningMarkerPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hostproc: removing running marker: %w", err)
	}

	return nil
}

// lookupPIDs returns the PIDs in pids keyed by any of names, skipping
// names that aren't present.
func lookupPIDs(pids map[string]int, names ...string) []int {
	out := make([]int, 0, len(names))

	for _, name := range names {
		if pid, ok := pids[name]; ok {
			out = append(out, pid)
		}
	}

	return out
}

// gracefulPodDeleteGracePeriodSeconds bounds how long kubelet is given to
// stop each pod's containers cleanly (SIGTERM, then SIGKILL) before moving
// on. Short by design: this is a disposable local cluster being torn down,
// not a production rolling deploy that needs to drain long-lived
// connections — CoreDNS, local-path-provisioner, and typical dev/test
// workloads all exit promptly on SIGTERM.
const gracefulPodDeleteGracePeriodSeconds = int64(5)

// gracefulPodStopTimeout bounds gracefulStopPods as a whole (listing pods,
// deleting them, and waiting for kubelet/CRI to actually finish tearing
// each one down). This is what lets containerd's shims exit cleanly
// instead of being orphaned by Stop's later hard SIGKILL: a shim is
// deliberately designed to survive its containerd daemon dying (shim v2's
// whole point is surviving a containerd restart/crash), so no amount of
// gracefully signaling containerd itself ever stops the shims it was
// supervising — only asking kubelet/CRI to tear down each pod while
// containerd is still alive does. Confirmed empirically (see
// test/benchmark/PROGRESS-robustness.md): a plain "kubectl delete pod"
// reliably stops the shim within a few seconds; talking to containerd's
// own task/sandbox client API directly did not.
const gracefulPodStopTimeout = 15 * time.Second

// podPollInterval governs gracefulStopPods' wait for pods to disappear.
const podPollInterval = 200 * time.Millisecond

// gracefulStopPods asks the cluster's own API server (via kubeconfigPath)
// to delete every pod in every namespace and waits, bounded by
// gracefulPodStopTimeout, for each one to actually disappear — see Stop's
// doc comment for why this (not a signal to containerd) is what avoids
// orphaning shim processes. Stop already stops kube-controller-manager and
// kube-scheduler before calling this, so nothing should recreate a pod
// while this is waiting for it to disappear.
//
// Entirely best-effort: any failure (API server unreachable, cluster
// already partially crashed before Stop ran, kubeconfig not written yet)
// is swallowed. Stop's subsequent hard process kill and the safety-net
// shim scan after it are what keep teardown correct even when this step
// can't run to completion — just not guaranteed to leave zero orphaned
// shims in that case, same as before this fix.
func gracefulStopPods(ctx context.Context, kubeconfigPath string) {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return
	}

	stopCtx, cancel := context.WithTimeout(ctx, gracefulPodStopTimeout)
	defer cancel()

	pods, err := clientset.CoreV1().Pods(metav1.NamespaceAll).List(stopCtx, metav1.ListOptions{})
	if err != nil || len(pods.Items) == 0 {
		return
	}

	grace := gracefulPodDeleteGracePeriodSeconds

	for i := range pods.Items {
		pod := &pods.Items[i]

		_ = clientset.CoreV1().Pods(pod.Namespace).Delete(stopCtx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
			Preconditions:      &metav1.Preconditions{UID: &pod.UID},
		})
	}

	waitPodsGone(stopCtx, clientset, pods.Items)
}

// waitPodsGone polls until every pod in pods — identified by the
// namespace/name/UID gracefulStopPods observed at the moment it listed
// them — is gone, or ctx is done. A pod whose name now resolves to a
// different UID (e.g. a controller already recreated it) counts as gone
// too: this only waits for the specific objects it asked to delete, never
// for whatever might replace them.
func waitPodsGone(ctx context.Context, clientset kubernetes.Interface, pods []corev1.Pod) {
	for {
		allGone := true

		for _, pod := range pods {
			current, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err == nil && current.UID == pod.UID {
				allGone = false

				break
			}
		}

		if allGone {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(podPollInterval):
		}
	}
}

// shimTaskGlob matches every containerd runtime-v2 task bundle directory a
// cluster's own containerd may have created. config.go's
// writeContainerdConfig scopes containerd's "state" dir to
// dataDir/containerd/state, so this can never match another cluster's
// bundles.
const shimTaskGlob = "containerd/state/io.containerd.runtime.v2.task/*/*"

// killOrphanedShims kills, by exact PID, every containerd-shim-runc-v2
// process still alive for a task bundle directory left under dataDir —
// the safety net for whenever gracefulStopPods couldn't run to completion.
//
// A shim process's current working directory is set to its own bundle
// directory for its entire lifetime — containerd/v2's runtime-v2 shim
// protocol documents this ("the start command ... has the bundle for the
// container set as the cwd"), confirmed empirically against this exact
// containerd/runc build during this fix's investigation (see
// test/benchmark/PROGRESS-robustness.md) — which gives an exact,
// non-pattern-matched way to identify which OS process, if any, belongs to
// a given bundle: never a name-based kill across every
// "containerd-shim-runc-v2" process on the host.
func killOrphanedShims(dataDir string) {
	bundles, err := filepath.Glob(filepath.Join(dataDir, shimTaskGlob))
	if err != nil {
		return
	}

	for _, bundle := range bundles {
		info, err := os.Stat(bundle)
		if err != nil || !info.IsDir() {
			continue
		}

		if pid, ok := findProcessByCwd(bundle); ok {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// findProcessByCwd scans /proc for a process whose current working
// directory is exactly wantCwd, returning its PID. Best-effort: /proc
// entries that vanish mid-scan (a process exiting concurrently) or that
// this process lacks permission to read are silently skipped, never
// treated as a match.
func findProcessByCwd(wantCwd string) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}

	want := filepath.Clean(wantCwd)

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		if err != nil {
			continue
		}

		if filepath.Clean(cwd) == want {
			return pid, true
		}
	}

	return 0, false
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
