# Robustness fixes: orphaned shim processes on delete + unbounded node-ready wait

Not committed; scratch state for this session. Two fixes, both already root-caused in
PROGRESS-m3prep.md (shim leak) and PROGRESS-incontainer.md (infinite-wait). Branch:
`m0-spikes` (feature branch, no worktree tool available to this session — editing directly
per this branch already being non-main).

## Fix 1: `rask delete` leaves orphaned containerd-shim-runc-v2 processes

### Root cause (from PROGRESS-m3prep.md, re-confirmed here)

`internal/substrate/hostproc/teardown.go`'s `Stop` SIGTERMs then SIGKILLs every tracked PID
(including containerd) after only a 300ms grace period, with no attempt to ask containerd to
gracefully stop the tasks/shims it's supervising first. containerd-shim-runc-v2 processes are
deliberately designed to survive a containerd daemon restart/crash (that's the whole point of
the shim v2 architecture), so killing containerd itself — gracefully or not — never stops its
shims. The only correct way to stop a shim is to ask containerd to delete its task (which tells
the shim to tear down and exit), before or instead of killing containerd.

### Investigation (live, in colima, real cluster + real pods — see below for the full trail)

1. `github.com/containerd/containerd/v2/core/runtime/v2`'s bootstrap protocol
   (`bootapi.BootstrapResult`, written to `<state>/io.containerd.runtime.v2.task/k8s.io/<id>/bootstrap.json`)
   carries only the shim's listening address/protocol, not its OS PID (read `core/runtime/v2/
   {binary,shim,shim_manager}.go` and `docs/runtime-v2.md` in the vendored
   `github.com/containerd/containerd/v2@v2.3.3` module). No plain "shim.pid" file exists.
2. Tried the generic `client.Task.Kill`+`Delete` API directly (like `ctr task kill`+`ctr task rm`)
   against a real pod's sandbox+container tasks: the container task deletes fine, but
   `core/runtime/v2/shim.go`'s `shimTask.delete` explicitly skips telling the shim to shut down
   when the task belongs to a CRI "sandboxed" pod ("Don't shutdown sandbox as there may be other
   containers running. Let controller decide when to shutdown") — confirmed empirically, the shim
   OS process stayed alive (state "S", PPID 1) indefinitely after this.
3. Tried the newer `client.Sandbox.Stop`+`Shutdown` API (the sandbox-controller path docs
   describe as "shutdowns shim instance") against the same live cluster: both calls returned
   success, but the shim OS process **still did not exit** — this containerd
   build's CRI plugin does not appear to route real shim lifecycle through that path the way the
   client-level API's doc comment implies (not investigated further; not worth more time given (4)
   below already gives a proven-working path).
4. **What actually works, confirmed live**: a completely normal `kubectl delete pod` (going
   through kubelet -> CRI's real `RemovePodSandbox`, exactly what already happens on every
   ordinary pod deletion) cleanly stops the shim process every time — verified by creating a
   single real pod, noting its shim PID, `kubectl delete pod --wait=true`, and polling `ps` for
   30s: the shim was already gone within the first 5s. This is unsurprising in hindsight (it's
   the standard, well-tested path every real Kubernetes cluster relies on) but the takeaway that
   matters here: **rask's fix should drive this exact same path** (delete every Pod through the
   cluster's own API server while kubelet/containerd are still alive) rather than reimplement shim
   lifecycle management against containerd's client API directly, which turned out to be far more
   fragile than expected (points 2-3 above).
5. Separately confirmed (via `/proc/<pid>/cwd`) that a running shim's cwd is always exactly its own
   bundle directory (`<dataDir>/containerd/state/io.containerd.runtime.v2.task/<ns>/<id>`) —
   matches `docs/runtime-v2.md`'s shim protocol description ("has the bundle for the container set
   as the cwd") exactly, and gives an exact (non-pattern-matched) way to find a specific orphaned
   shim's OS PID for the safety-net step below.

### Plan (revised after the investigation above)

1. Primary fix, in this order inside `Stop`:
   a. SIGTERM (bounded grace) + SIGKILL `kube-controller-manager` and `kube-scheduler` first —
      before touching any pod — so nothing recreates a pod (e.g. a Deployment's ReplicaSet
      controller replacing a deleted CoreDNS pod) while the next step is waiting for it to
      disappear.
   b. While apiserver/kubelet/containerd are still alive: list every pod (all namespaces) via the
      cluster's own admin kubeconfig, `Delete` each with a short grace period and a UID
      precondition, and wait (bounded) for each specific pod object to disappear (or be replaced
      by a different UID under the same name, which counts as "gone" — we only care about the
      pods we asked to delete, never whatever a controller might spin up after). This is exactly
      the path point 4 above proved reliably stops shims. Best-effort/bounded throughout: any
      failure (API server unreachable, cluster already partially crashed before `Stop` ran) is
      swallowed, falling through to the existing hard-kill path.
   c. SIGTERM (bounded grace) + SIGKILL every remaining tracked PID (kubelet, kube-proxy,
      kube-apiserver, containerd, kine) — same as before this fix.
2. Safety net: after the hard-kill above, scan containerd's own state dir
   (`dataDir/containerd/state/io.containerd.runtime.v2.task/*/*`) for any task bundle directories
   still present and, for each, find the OS process whose `/proc/<pid>/cwd` is exactly that bundle
   directory (point 5 above) and kill it by that exact PID — never a name-pattern kill. Covers
   whatever step 1b couldn't reach (its own timeout, or a cluster that was already partially
   crashed before `Stop` ran) and pre-existing orphans tied to this exact cluster from any earlier
   run.
3. Verify E2E in colima: create -> run a real pod -> delete -> zero shim/containerd/kubelet
   processes remain (ps snapshot before/after).

## Fix 2: node-ready wait can hang forever

### Root cause (from PROGRESS-incontainer.md, re-confirmed here)

`internal/bootstrap/boot.go`'s `runBootDAG` and its phase helpers (`bootKubelet`,
`bootKubeProxy`, `bootControlPlane`, `bootDatastoreAndControlPlane`, the containerd branch, and
`watchNodeReady`) each poll a readiness condition using `waitCtx` (errgroup-derived from Boot's
caller ctx), which has no deadline of its own — only `cmd/rask/create.go`'s `--wait=coredns`
path has a bound (`coreDNSWaitTimeout`), and that only starts counting after node-ready is
already reached. Any one phase that never becomes healthy (bad cgroup driver, missing device,
crash-looping component — real operator errors, not hypothetical) hangs `rask create` forever.

### Plan

- Add named, generous default timeouts (mirroring `coreDNSWaitTimeout`'s precedent) for every
  readiness wait in the boot DAG: datastore, containerd socket, apiserver readyz,
  controller-manager/scheduler healthz, kubelet healthz, kube-proxy healthz, node-ready watch.
- Thread each as an explicit `timeout time.Duration` parameter into the relevant unexported boot
  phase function (mirrors this repo's existing `internal/substrate/vz/watchdog.go`
  `runBootWatchdog(ctx, cancel, agentHostPort, timeout, failed)` pattern — a named constant at the
  call site, a testable parameter underneath), rather than hardcoding the constant inside the
  function body.
- Improve `waitHTTPOK`/`waitUnixSocket` (and kine's own copy of `waitUnixSocket`) to track and
  surface the last poll error in the timeout error, not just `ctx.Err()`, so a bounded timeout's
  error message actually explains what was wrong (e.g. "connection refused" vs "500") instead of
  just "context deadline exceeded".
- `watchNodeReady`'s clientset parameter changes from the concrete `*kubernetes.Clientset` to the
  `kubernetes.Interface` it actually only needs (matches `internal/manifests.WaitDeploymentReady`'s
  existing precedent), so its timeout path is unit-testable against `client-go`'s fake clientset
  without a real API server.
- Unit tests for the timeout paths: the two primitives (`waitHTTPOK`/`waitUnixSocket`) thoroughly,
  plus one representative boot-phase test per distinct wait shape (`bootKubelet` for the
  process-healthz shape cited directly in the task at boot.go:410, `bootContainerd` for the
  socket-wait shape, `watchNodeReady` for the watch-based shape) — not a near-duplicate test for
  every phase, since `bootKubeProxy`/`bootControlPlane`/`bootDatastoreAndControlPlane`'s apiserver
  wait are mechanically the same wrapped-waitHTTPOK shape already covered by the primitive's own
  tests plus the `bootKubelet` wiring test. The DAG's overall wiring (that a stuck phase doesn't
  hang the whole `rask create`) is additionally verified by a real forced-failure E2E run in colima.
- Audited the rest of the boot DAG and adjacent packages for other unbounded waits
  (`internal/store/kine`, `internal/manifests`, `internal/components`, `internal/substrate/vz`):
  - `internal/store/kine.Datastore.Start`'s own `waitUnixSocket` was unbounded before this session
    (same underlying issue) — fixed by `bootDatastoreAndControlPlane` now wrapping the ctx it
    passes to `Datastore.Start` with `datastoreReadyTimeout`.
  - `internal/manifests.WaitDeploymentReady` was already bounded at its only call site
    (`cmd/rask/create.go`'s `coreDNSWaitTimeout`) before this session; no change needed.
  - `internal/components/{cache,iptables}.go`'s `for {}` loops are tar-extraction loops that
    terminate on `io.EOF`, not readiness polls — not unbounded waits.
  - `internal/substrate/hostproc/exec.go`'s `for {}` is a `listener.Accept()` loop tied to `ctx`
    (returns once `ctx.Err() != nil`) — already bounded by whatever ctx its caller supplies.
  - `internal/substrate/vz` (code-inspection only, no vz E2E run yet — see below): `vz.go`'s
    `Start` already wraps the entire in-guest boot wait (including the guest's own
    `internal/bootstrap.Boot` call, reached indirectly via the agent's healthz) in
    `bootCtx` bounded by `bootTimeout = 5 * time.Minute`; `vmhost.go`'s `RunVMHost` additionally
    runs its own `bootWatchdogTimeout = 6 * time.Minute` inside the guest-side process
    independently of the host. Both were already bounded before this session — no vz code change
    needed for fix 2. This session's `internal/bootstrap` changes (shared by both hostproc and
    vz's in-guest boot path) additionally bound the *individual* phases within that already-bounded
    window, which is a strict improvement (a stuck phase now fails with a specific phase-named
    error well before either outer 5/6-minute deadline, instead of only being caught by them).

## Status: both fixes implemented and verified

### Fix 1 implementation

`internal/substrate/hostproc/teardown.go`'s `Stop` now:

1. SIGTERMs (bounded `stopGracePeriod`) then SIGKILLs `kube-controller-manager`/
   `kube-scheduler` first (`lookupPIDs`), so nothing recreates a pod the next step deletes.
2. Calls `gracefulStopPods` (new): lists every pod (all namespaces) via the cluster's own admin
   kubeconfig, deletes each with a UID precondition and a short grace period
   (`gracefulPodDeleteGracePeriodSeconds = 5`), and waits (`waitPodsGone`, bounded by
   `gracefulPodStopTimeout = 15s`) for each specific pod object to disappear. Entirely
   best-effort — any failure (unreachable API server, kubeconfig missing, cluster already
   partially crashed) is swallowed and falls through to the existing hard-kill.
3. Hard-kills every remaining tracked PID as before.
4. Calls `killOrphanedShims` (new): globs `dataDir/containerd/state/io.containerd.runtime.v2.task/*/*`
   and, for each bundle dir still present, finds the exact OS process whose `/proc/<pid>/cwd`
   equals that bundle dir (`findProcessByCwd`) and SIGKILLs it by that exact PID.
5. Unmount + CNI bridge removal, unchanged.

### Fix 1 verification

Unit tests (real processes, no root needed beyond what the test suite already assumes):
`TestLookupPIDs_*`, `TestFindProcessByCwd_*`, `TestKillOrphanedShims_*` in
`internal/substrate/hostproc/teardown_test.go` — all pass (`go test`, cross-compiled
linux/arm64, run as root in colima; race detector unavailable for cross-arch CGO builds from
this darwin host, matching this repo's own prior precedent, see PROGRESS.md).

Live E2E in colima, 2026-07-31: `rask create cluster --name e2efix --wait coredns` (boot
latency unaffected: `node_ready` 1.7s, matches prior baselines) -> `kubectl run smokefix
--image=pause:3.10` (real pod, alongside CoreDNS/local-path-provisioner) -> ps snapshot showed
2 shims + 12 rask-owned processes for the cluster -> `rask delete cluster --name e2efix`
(7.2s wall clock including SSH overhead) -> **zero** processes or shims reference `e2efix`
afterward, `cni0` bridge removed, cluster dir gone. Total host shim count returned to the
session's pre-existing baseline (67 — unrelated orphans from earlier, different sessions,
deliberately left untouched per this session's environment rules).

### Fix 2 implementation

`internal/bootstrap/boot.go` gained named bounded timeouts (`datastoreReadyTimeout`,
`containerdReadyTimeout`, `apiserverReadyTimeout`, `controlPlaneReadyTimeout`,
`kubeletReadyTimeout`, `kubeProxyReadyTimeout`, `nodeReadyTimeout`), each threaded as an
explicit `timeout time.Duration` parameter into its phase function (mirrors
`internal/substrate/vz/watchdog.go`'s existing `runBootWatchdog(ctx, cancel, ..., timeout,
failed)` pattern), wrapped around the relevant `waitHTTPOK`/`waitUnixSocket`/`watchNodeReady`
call via `context.WithTimeout(waitCtx, timeout)`. The containerd branch (previously inlined in
`runBootDAG`) was extracted into `bootContainerd` for the same reason every other phase is
already its own function: testability. `bootDatastoreAndControlPlane` also now wraps the ctx it
passes to `cfg.Datastore.Start` (closing the same gap in `internal/store/kine.Datastore.Start`,
which was unbounded before this session). `watchNodeReady`'s clientset parameter widened from
the concrete `*kubernetes.Clientset` to `kubernetes.Interface` (matches
`internal/manifests.WaitDeploymentReady`'s existing precedent) so its timeout path is
unit-testable against `client-go`'s fake clientset.

`waitHTTPOK`/`waitUnixSocket` (`internal/bootstrap/readiness.go`) and kine's own
`waitUnixSocket` (`internal/store/kine/kine.go`) now track and surface the last poll
error/status in their timeout error, not just `ctx.Err()`.

### Fix 2 verification

Unit tests: `TestWaitHTTPOK_TimeoutErrorIncludesLastStatus`,
`TestWaitHTTPOK_TimeoutErrorIncludesLastDialError`,
`TestWaitUnixSocket_TimeoutErrorIncludesLastDialError` (readiness_test.go);
`TestBootContainerd_TimesOutWhenSocketNeverAppears`,
`TestBootKubelet_TimesOutWhenHealthzNeverReady` (the exact line the task cited, boot.go:410),
`TestWatchNodeReady_TimesOutWhenNodeNeverBecomesReady` +
`TestWatchNodeReady_ReturnsNilOnceNodeBecomesReady` (new `boot_timeout_test.go`, covering all
three distinct wait shapes in the DAG — polled-HTTP, polled-socket, and watch-based). Not
duplicated per-phase for `bootKubeProxy`/`bootControlPlane`/`bootDatastoreAndControlPlane`'s
apiserver wait since they're mechanically the same wrapped-`waitHTTPOK` shape already covered by
the primitive's own tests plus the `bootKubelet` wiring test — matches this repo's own stated
precedent (PROGRESS-m3prep.md: "thin orchestration wrappers rely on their callees' own unit
tests plus a real E2E run, not a mocked integration test"). Full `internal/bootstrap` suite:
`go test -race -shuffle=on -count=1 ./internal/bootstrap/...` — pass, no regression.

Live forced-failure E2E in colima, 2026-07-31: bound a dummy `python3` listener on
`127.0.0.1:10248` (kubelet's healthz port) before `rask create cluster --name forcefail --wait
node --verbose`. Result: failed after ~62s (not a hang) with:

```
cluster "forcefail": hostproc: bootstrap: kubelet did not become ready within 1m0s: waiting for
http://127.0.0.1:10248/healthz to become healthy: context deadline exceeded (last error: Get
"http://127.0.0.1:10248/healthz": context deadline exceeded)
```

Confirms both the phase name and a real last-error are present, and the process exits on its
own (`rc=1`) well before the `timeout 90` wrapper would have needed to intervene — this is
exactly trial 2/4's hang from PROGRESS-incontainer.md, now bounded.

**Tangential finding, not investigated further, out of scope for this session's two fixes**:
cleaning up after this forced-failure run surfaced a separate, pre-existing gap — a `Boot()`
failure (as opposed to a later `hostproc.Start` step failing after `Boot()` succeeds) is handled
entirely inside `bootstrap.Boot` itself (`sup.Stop()`, a hard kill with no graceful pod stop,
since `hostproc.Runtime.Stop`'s new logic in this fix only runs from a later, separate `rask
delete` invocation) and never writes `state.json`, so a subsequent `rask delete` on that
same-named cluster finds no state to act on and its plain `os.RemoveAll(dataDir)` in `Delete`
can fail on a still-busy mount (observed: `unlinkat ... device or resource busy` on a pod's
projected-token tmpfs mount). Worked around manually this session (lazy-unmount every mount
under the cluster's data dir, kill the one resulting orphaned shim by exact PID, then remove the
directory) rather than left broken on the shared VM. Whether real pods were actually running
under `forcefail` despite kubelet's own healthz probe never answering was not root-caused (kubelet
may treat a healthz-listener bind failure as non-fatal to its main sync loop — plausible but not
confirmed by reading kubelet's source this session). Worth a dedicated follow-up: `Boot`'s own
failure path (or `hostproc.Start`'s) should persist enough state for a subsequent `Stop`+`Delete`
to clean up a partially-booted cluster the same way this fix now cleans up a fully-booted one.

## Files changed

- `internal/substrate/hostproc/teardown.go`, `teardown_test.go` (Fix 1)
- `internal/bootstrap/boot.go`, `boot_timeout_test.go` (new), `readiness.go`, `readiness_test.go` (Fix 2)
- `internal/store/kine/kine.go` (Fix 2, matching `waitUnixSocket` improvement)

Verification commands run: `go build ./...` (darwin + linux/arm64 cross-compile), `go vet ./...`
(both), `golangci-lint run ./internal/bootstrap/... ./internal/store/kine/...
./internal/substrate/hostproc/...` (0 issues), `go test -race -shuffle=on -count=1 ./...` (darwin,
all packages pass), full `internal/substrate/hostproc` suite run as root in colima (linux/arm64,
no `-race` — cross-arch CGO unavailable from this host, same limitation noted in PROGRESS.md).
No git commits made this session, per instructions.
