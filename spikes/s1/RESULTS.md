# S1 results: stock components, no kubeadm, kine/SQLite

## Verdict

**The core bet holds.** A single-node cluster built from stock
`kube-apiserver` + `kube-controller-manager` + `kube-scheduler` + `kubelet` +
`containerd` + `kine`/SQLite, launched directly as child processes (no
kubeadm, no static pods), reaches **node Ready in ~2.4-3.2s**, comfortably
under the 5s target, both cold and with a prebaked seed datastore.

| scenario | p50 | p95 | n |
|---|---|---|---|
| cold (empty kine/SQLite) | **2.978s** | 3.118s | 10 |
| prebaked seed | **2.409s** | 2.597s | 10 |

## Environment (hardware caveat)

- Host: Apple Silicon macOS, colima VM (macOS Virtualization.framework, virtiofs mounts, docker runtime)
- VM: Ubuntu 24.04.4 LTS, aarch64, **2 vCPU**, 16GiB, cgroup v2 unified (systemd as PID 1, cgroupfs driver used for our containerd/kubelet), swap off
- This VM is shared with the user's other colima/docker workloads (several unrelated kind clusters were running throughout these measurements; they were unaffected — see "isolation" below)
- **Real Linux hardware (bare metal or a dedicated CI runner with more vCPUs) should be faster than these numbers**, since apiserver bootstrap is CPU-bound (RBAC/CRD reconciliation, admission plugin init) and this VM has only 2 vCPUs shared with other processes
- Component versions: kube-apiserver/controller-manager/scheduler/kubelet `v1.33.13`, kine `v0.16.3`, containerd `2.3.3`, runc `v1.5.1`, containernetworking/plugins `v1.9.1` (all linux/arm64)

## Methodology

- `spikes/s1/main.go` (module `rask-spike-s1`, independent `go.mod`, does not touch the rask repo's root module) generates a fresh ECDSA P-256 PKI (CA, apiserver serving cert, service-account signing keypair, admin/kubelet/controller-manager/scheduler client certs+kubeconfigs) per run, writes CNI (bridge+host-local+portmap conflist, loopback binary present for containerd's internal use) and containerd config, then boots the component graph as a DAG of goroutines synchronized with channels:
  ```
  kine -> apiserver ---+--> controller-manager + scheduler
                       |
  containerd ----------+--> kubelet --> node registered --> node Ready [--> pod Running]
  ```
- Each phase transition is timestamped (`t0` = moment kine is spawned; PKI generation and config file writes happen before `t0` and are not counted — they are sub-millisecond and not part of the "boot a running cluster" claim)
- Node Ready is detected via a real `client-go` watch on the Node object (not polling), matching how an orchestrator would actually gate on this in production
- 10 runs per scenario, full teardown between runs: SIGTERM then SIGKILL the whole process group of every component (catches containerd shims), lazy-unmount anything left under datadir, wipe datadir, delete the CNI bridge/leftover netns
- `--datadir` defaults to `/var/lib/rask-spike-s1` — a **native VM filesystem path**, not the virtiofs-shared `$HOME`. This was a deliberate architectural choice, not an oversight (see gotchas below)
- Seed generation: one clean run, then SIGTERM to kine (checkpoints its SQLite WAL) and copy `state.db` out; the seeded runs copy that file into place before starting kine

## Phase breakdown

### Cold (empty datastore), n=10

| phase (cumulative since t0) | p50 | p95 |
|---|---|---|
| kine_up | 72ms | 90ms |
| containerd_up | 87ms | 110ms |
| apiserver_readyz | 2.258s | 2.329s |
| kubelet_started | 2.574s | 2.728s |
| cm_sched_started | 2.694s | 2.843s |
| node_registered | 2.757s | 2.908s |
| **node_ready (TOTAL)** | **2.978s** | **3.118s** |

### Prebaked seed, n=10

| phase (cumulative since t0) | p50 | p95 |
|---|---|---|
| kine_up | 66ms | 84ms |
| containerd_up | 88ms | 110ms |
| apiserver_readyz | 1.717s | 1.893s |
| kubelet_started | 1.993s | 2.173s |
| node_registered | 2.195s | 2.373s |
| cm_sched_started | 2.194s | 2.426s |
| **node_ready (TOTAL)** | **2.409s** | **2.597s** |

## What the seed actually saved

The seed's entire benefit (**~569ms p50, ~521ms p95, ~19%/17%**) lands almost
exactly on the `apiserver_readyz` phase (2.258s -> 1.717s, ~541ms), and
nowhere else. Every downstream phase's *delta* (kubelet start, node
registration, node Ready) is essentially unchanged between cold and seeded
runs. This makes sense: the seeded SQLite already contains the RBAC
bootstrap ClusterRoles/ClusterRoleBindings and the four default namespaces
(`default`, `kube-system`, `kube-public`, `kube-node-lease`) that
kube-apiserver's built-in post-start hooks otherwise create from scratch on
every cold boot. **Prebaking does not appreciably speed up kine, containerd,
kubelet startup, or the kubelet->apiserver node registration handshake** —
only apiserver's own internal bootstrap reconciliation.

## Top bottleneck: apiserver bootstrap, not kine/containerd/kubelet

`apiserver_readyz` is **~75% of total wall time** in both scenarios
(2.258s of 2.978s cold; 1.717s of 2.409s seeded). kine, containerd, and
`/readyz`-adjacent kubelet/CM/scheduler startup are each under a few hundred
milliseconds and run in parallel. The dominant cost is kube-apiserver's own
in-process initialization (loading ~40 API group/versions into the resource
manager, admission plugin chain init, RBAC/namespace bootstrap post-start
hooks, dynamic serving-cert/CA-bundle controllers) before `/readyz` goes
green — this is CPU-bound work inside the apiserver binary itself, not I/O
or storage-backend latency. On a 2-vCPU shared VM this is the most likely
lever for further gains (parallelism is already maxed for this stage since
apiserver startup is single-process); real multi-core hardware should show
a meaningfully lower `apiserver_readyz` number.

## Stretch: smoke pod (informational only, not part of the <5s claim)

One sample with `--with-pod`: `node_ready` at 3.105s, `pod_running` at
9.732s (**+6.6s**). This delta is not part of the core measurement (each run
wipes containerd's image cache, so this is always a cold `registry.k8s.io/pause:3.10`
pull over the network) plus the node-lifecycle-controller's initial taint-removal
reconcile lag (a few hundred ms — see gotcha below). Not repeated 10x since it is
stretch/informational per the spike's scope, and its cost is dominated by
image-pull network latency rather than anything rask's own design controls.

## Architectural surprises / gotchas (relevant to rask's design)

1. **`--anonymous-auth=false` breaks unauthenticated `/readyz` probes.**
   Authentication happens before the "always-allow" authorization exemption
   for health endpoints — a request with no credentials gets 401 before it
   ever reaches the exemption. Any orchestrator (rask included) that polls
   `/readyz` while running the apiserver with anonymous auth disabled must
   present a valid client certificate (we used the admin `system:masters`
   cert) or the poll loop will time out with no useful log signal on the
   apiserver side (401s aren't logged at default verbosity).

2. **`--use-service-account-credentials=false` silently breaks node taint
   removal.** With it false, every kube-controller-manager control loop
   (including node-lifecycle-controller) runs as the single
   `system:kube-controller-manager` identity, and the built-in RBAC
   bootstrap policy does **not** grant that identity `nodes` write access —
   only the per-controller service accounts (`system:controller:node-controller`,
   etc.) get that via their own dedicated ClusterRoleBindings. Without
   `--use-service-account-credentials=true`, the node registers and goes
   Ready, but the `node.kubernetes.io/not-ready:NoSchedule` taint the
   `TaintNodesByCondition` admission plugin adds at Node creation is never
   removed, so pods never schedule. This is a real trap for any "roll your
   own control plane" tool: node Ready alone is not sufficient to prove
   pods will schedule.

3. **containerd 2.x split the CRI plugin's config schema.** `containerd
   config default` on 2.3.3 uses `io.containerd.cri.v1.images` /
   `io.containerd.cri.v1.runtime` (not the single `io.containerd.grpc.v1.cri`
   block from containerd 1.x). Any tool generating containerd config
   programmatically needs to either pin a containerd major version or branch
   on `containerd --version`.

4. **The `loopback` CNI plugin binary is required even though nothing
   references it in the conflist.** containerd's CRI plugin invokes it
   internally to bring up each pod sandbox's `lo` interface
   (`use_internal_loopback = false` is the default); only `bridge` +
   `host-local` + `portmap` need to appear in the conflist itself.

5. **datadir must be on a native VM filesystem, not virtiofs.** We
   deliberately defaulted `--datadir` to `/var/lib/rask-spike-s1` (native
   ext4, not the virtiofs-shared `$HOME`) because containerd's overlayfs
   snapshotter needs real Linux filesystem semantics. This directly informs
   rask's macOS design: per-cluster VM state (kine db, containerd
   root/state, kubelet root) must live on the VM's own virtio-blk-backed
   disk, never on a virtiofs share back to the host — consistent with the
   Apple container / virtiofs image-unpack pathology already noted in
   research-m0-spikes.md for a different reason (image unpack), but it
   turns out to matter for the control-plane datastore too.

6. **No kubeadm-specific latency floor exists in this path.** Everything
   that made `kind` slow (kubeadm phases, static pod convergence polling)
   is simply absent here — kubelet is launched directly with a
   `KubeletConfiguration` file and never touches a static pod manifest
   directory. This directly confirms the BenTheElder kind-maintainer
   claim in research-m0-spikes.md ("kubeadm is the floor for kind's speed")
   by demonstrating the floor moves dramatically once kubeadm is removed
   entirely, not just tuned.

## Reproducing

```bash
cd spikes/s1
./fetch.sh                     # downloads binaries into work/ (gitignored)
GOOS=linux GOARCH=arm64 go build -o spike-s1 .
colima ssh -- sudo ./spike-s1 --runs 10                       # cold
colima ssh -- sudo ./spike-s1 --runs 1 --save-seed /path/to/seed.db
colima ssh -- sudo ./spike-s1 --runs 10 --seed /path/to/seed.db  # prebaked
colima ssh -- sudo ./spike-s1 --runs 1 --with-pod              # stretch
```
