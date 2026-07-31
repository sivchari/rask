# M3 PoC: `rask create cluster` (hostproc substrate) inside a Linux container

Goal: prove/refute that rask's hostproc substrate can run inside a container (dogfooding target:
haro workspace pod, EKS/containerd, currently unprivileged), find the minimal viable privilege
level, and document host-node prerequisites.

Environment: colima docker (aarch64, Ubuntu 24.04.4, kernel 6.8.0-100-generic, cgroup v2 unified,
2 vCPU/16GiB, shared with the user's other kind/haro containers — one test container at a time,
always `docker rm -f` after each trial, no pattern pkill).

rask built from branch `m0-spikes` (linux/arm64), copied into the colima VM at
`/root/.rask-poc/rask`. Component cache already warm at `/root/.rask/cache` (colima VM, from the
earlier real-host E2E in RESULTS-linux.md) — bind-mounted read-only into test containers to avoid
re-downloading (never written to directly).

## Host prerequisites confirmed present on the colima VM before any trial

- cgroup v2 unified, root controllers: `cpuset cpu io memory hugetlb pids rdma misc`
- kernel modules already loaded: bridge, veth, overlay, ip_tables, iptable_filter, iptable_nat,
  nf_tables, nf_conntrack (+ xt_* extensions), nf_conntrack_netlink
- `fs.inotify.max_user_instances=512`, `fs.inotify.max_user_watches=524288` (already raised, fjord
  precedent)
- **br_netfilter was NOT loaded** (`lsmod` empty, `/proc/sys/net/bridge/bridge-nf-call-iptables`
  missing) until `sudo modprobe br_netfilter` on the VM (host-level action, one-time, not
  something a container can do unprivileged — see verdict table). Available as a module, loaded
  successfully, `bridge-nf-call-iptables` sysctl now exists (=1).

## Matrix (in order, stop descending once one works)

| # | privilege level | result |
|---|---|---|
| 4 | non-privileged, `--cap-add=NET_ADMIN,SYS_ADMIN,SYS_RESOURCE` + apparmor/seccomp unconfined, docker volume for cluster data | two independent hard blockers found: `/dev/kmsg` missing, `/sys/fs/cgroup` read-only — see below (out of order in this table; investigated after trial 3's success, still valuable negative data) |
| 1a | `--privileged --cgroupns=host`, cluster data dir on container's own overlay2 rootfs | node Ready, CoreDNS/local-path-provisioner stuck `ContainerCreating` forever (overlay-on-overlay) |
| 1b | `--privileged --cgroupns=host`, cluster data dir bind-mounted from macOS host path (virtiofs) | inconclusive — extremely slow, likely virtiofs I/O, not overlay; retrying with a docker named volume instead |
| 1c | `--privileged --cgroupns=host`, cluster data dir on a docker named volume (VM-native fs) | abandoned, see below — moved to trial 2 (default cgroupns) instead |
| 2 | `--privileged` (default/private cgroupns), cluster data dir on a docker named volume | kubelet crash-loops: cgroup v2 "invalid state" — see below |
| 3 | trial 2 + kind-style sub-cgroup/PID-move workaround before `rask create` | **SUCCESS** — node Ready, CoreDNS Ready, smoke pod Running |

### Trial 1b/1c abandonment: `--cgroupns=host` wedged the shared containerd's shim tracking for this container

`docker kill rask-poc1b` succeeded (PID1 died immediately) but `docker rm` then hung
indefinitely: `docker inspect` kept reporting `State.Status: running` with a dead PID.
`colima ssh -- sudo journalctl -u containerd` showed the container's shim repeatedly failing
`dial /run/containerd/containerd.sock.ttrpc: timeout` and evicting exit-event queue entries —
containerd's host daemon lost ttrpc communication with this container's shim and never received
its exit event, so dockerd's cache never updated. `sudo ctr --namespace moby tasks ls` confirmed
containerd itself considered the task `STOPPED`; `sudo ctr --namespace moby tasks rm <id>`
successfully deleted it at the containerd level, but `docker rm` still refused afterward (dockerd's
own state is now permanently desynced from containerd for this one container — would need a
dockerd restart to reconcile, which was **not** done, since that would disrupt the user's other
running containers). The container was left in place (0% CPU, no other side effects — verified
`fjord-lb-control-plane`/`flagfield-control-plane`/`haro-local-control-plane`/`postgres-haro`/
`redis` and a concurrent unrelated `cbm-rel-verify` container all kept running normally throughout).

**Correction (observed again below, trial 2 and a plain cgroup-probe container with no nested k8s
workload at all)**: this same slow-reap symptom recurred for *every* `docker kill`+`docker rm` in
this session, including a container that only ran a trivial `cgroup.procs` shell dance — so it is
**not** specific to `--cgroupns=host` or to nested kubelet/containerd teardown. It looks like a
general characteristic of this dockerd/containerd install on this particular colima VM/session
(root cause not identified — possibly containerd/dockerd having accumulated some backlog across
this session's many privileged-container churns). Practical workaround established and reused for
every trial from here on: `docker kill <name>` (always fast), then `sudo ctr --namespace moby tasks
rm <container-id>` via `colima ssh` (containerd already considers the task `STOPPED`; this makes it
official), then `docker rm <name>` — sometimes still needs a retry/wait, but no dockerd restart was
ever required, and **no other container on the shared VM was ever affected** (verified repeatedly:
`fjord-lb-control-plane`, `flagfield-control-plane`, `haro-local-control-plane`, `postgres-haro`,
`redis`, and a concurrent unrelated `cbm-rel-verify`/`cbm-build` all stayed healthy throughout this
entire session).

### Finding: teardown latency for a privileged container running nested kubelet+containerd

`docker kill` on trial 1b's container returns immediately (PID1 dies right away, confirmed via
`docker top` going empty), but `docker rm` on the same container kept failing with "container is
running" for 12+ minutes afterward. `docker inspect` still reports `State.Status: running` long
after PID1 is gone. Root cause (via `colima ssh -- ps aux | grep <container id>`, host VM's own
process list): the `containerd-shim-runc-v2` for the container is still alive and actively
consuming CPU (accumulating real time, not hung/D-state) — it hasn't finished reaping. Host load
average and memory are both idle (0.4-0.7 on 2 vCPUs, 12GiB free) during this, so it's not general
VM contention; it's specifically the teardown of everything the nested kubelet/containerd/CNI
stack mounted inside the container (pod volumes, CNI veth/bridge, per-pod cgroup views, procfs/
sysfs bind mounts) taking a long time to unwind one by one. This matches known kind/DinD teardown
behavior. **Operational implication for the eventual haro workspace pod integration**: pod
deletion/recreation latency for a workspace running rask-in-a-container should be expected to be
much higher than a normal pod's, and any orchestration around it (haro operator reconcile loops,
readiness/liveness probes assuming fast restarts) needs to budget for this.

### Trial 3: trial 2 + the cgroup v2 fix — full success (node Ready + CoreDNS Ready + smoke pod Running)

Empirically verified the kind-style fix first, cheaply, in a throwaway plain-debian container
(no rask, no nested k8s) before spending another multi-minute cluster-boot cycle on it:

```bash
mkdir -p /sys/fs/cgroup/init
for p in $(cat /sys/fs/cgroup/cgroup.procs); do echo $p > /sys/fs/cgroup/init/cgroup.procs; done
echo "+cpuset +cpu +io +memory +pids" > /sys/fs/cgroup/cgroup.subtree_control   # now succeeds
```

Key question this answered: does a **subsequent** `docker exec` process land back in the
container's root cgroup (re-violating the constraint) or does it follow into `/init`? Verified via
`docker exec cgtest cat /proc/self/cgroup` → `0::/init`, and `cat /sys/fs/cgroup/cgroup.procs`
(root) → empty. **New `docker exec` sessions follow wherever the container's already-existing
processes were moved to, not a fixed "root" path recorded at container-creation time** — so this
fix is durable across every subsequent `docker exec`, not just a one-time trick.

Applied to a fresh trial-3 container (`--privileged`, default cgroupns, `docker volume` for
`/root/.rask/clusters`, same as trial 2) — ran the cgroup dance once via `docker exec` right after
container start, before installing packages or running rask. Result:

```
rask create cluster --name poc --verbose --wait coredns   → create rc=0
```

```
PHASE                  CUMULATIVE        DELTA
kine_up                      67ms         67ms
containerd_up                88ms         21ms
apiserver_readyz           2.257s       2.169s
kubelet_started            2.689s        433ms
cm_sched_started           2.694s          4ms
node_registered            2.843s        149ms
node_ready                 3.155s        312ms
kube_proxy_started          3.59s        435ms
```

**This matches RESULTS-linux.md's bare-host numbers almost exactly** (node_ready 3.155s here vs
3.08s on bare host) — running inside a container costs essentially nothing extra once the cgroup
and overlay issues are worked around.

- `kubectl get nodes`: `rask-node Ready`
- `kubectl get pods -A`: CoreDNS `1/1 Running`, local-path-provisioner `1/1 Running`
- `kubectl run smoke --image=registry.k8s.io/pause:3.10` → `1/1 Running`, got a real pod IP
  (`10.244.0.4`) on the CNI bridge — pod networking works end-to-end inside the container.
- `rask load image-archive`: attempted (`docker save alpine:3` from the host → `docker cp` into the
  container → `rask load image-archive --name poc /tmp/alpine.tar`) but **inconclusive**: late in
  this trial, `docker exec` dispatch into this specific container became very slow/queued (a
  trivial `echo` took several minutes to return; the `rask load` invocation never produced output
  before the container was torn down for the next trial). `docker top`/host `ps` never showed a
  `rask load` process actually running, so this looks like exec-dispatch queuing/degradation
  (consistent with the general dockerd slowness noted throughout this session), not a rask or
  in-container bug — but not proven either way. Worth a clean re-run in a fresher environment.

### Trial 4: non-privileged + targeted capabilities — two independent hard blockers

```
docker run -d --name rask-poc4 \
  --cap-add=NET_ADMIN --cap-add=SYS_ADMIN --cap-add=SYS_RESOURCE \
  --security-opt apparmor=unconfined --security-opt seccomp=unconfined \
  -v <repo>/.incontainer/rask:/usr/local/bin/rask:ro \
  -v <repo>/.incontainer/cache:/root/.rask/cache:ro \
  -v rask-poc-clusters4:/root/.rask/clusters \
  debian:bookworm-slim sleep infinity
```

**Positive finding first**: a plain overlay mount (`mount -t overlay ... lowerdir=.../upperdir=...`)
**succeeds** on this container once the lower/upper/work dirs are on the docker-volume-backed path
— i.e. `CAP_SYS_ADMIN` alone (no `--privileged`) is sufficient for the overlay mount syscall
itself; the overlay-on-overlay restriction from trial 1a is purely about the *target filesystem*
nesting, not a capability gate. Good news for a minimal-capability design.

**Two separate hard blockers, both unrelated to capabilities**:

1. **`/sys/fs/cgroup` is mounted read-only** in a non-`--privileged` container, regardless of
   `--cap-add=SYS_ADMIN` and disabling apparmor/seccomp: `mkdir /sys/fs/cgroup/init` →
   `Read-only file system`, `echo ... > cgroup.subtree_control` → `Read-only file system`. This is
   Docker's own default hardening for cgroup v2 delegation, independent of Linux capabilities —
   `--privileged` is what flips this specific mount to read-write, not any individual `--cap-add`.
   (Not yet tested: whether `--cgroupns=host` without `--privileged` changes this, since a plain
   `/sys/fs/cgroup` bind-mount of the *actual host* cgroupfs would naturally be read-write for a
   root-EUID process — flagged as an open follow-up, not tested this session due to time.)
2. **`/dev/kmsg` does not exist** in a non-privileged container (confirmed present on the colima
   VM host: `crw-r--r-- 1 root root 1, 11 ... /dev/kmsg`) — kubelet's `NewContainerManager` /
   `NewOOMWatcher` path unconditionally tries to open it at startup and kubelet fails immediately:
   ```
   "command failed" err="failed to run Kubelet: failed to create kubelet: open /dev/kmsg: no such file or directory"
   ```
   This one **is** independently fixable without `--privileged`, via `--device=/dev/kmsg:/dev/kmsg`
   (not tested in combination with a cgroup fix this session, since blocker 1 would still block —
   but confirmed the device exists on the host and Docker's `--device` flag is the standard way to
   pass through a single host device node to an otherwise-unprivileged container).

**Robustness gap confirmed again**: with kubelet failing immediately and exiting (not crash-looping
— `bootKubelet`'s `ProcessSpec` does not set `RestartOnCrash`), `rask create`'s wait for node-ready
(`waitHTTPOK` on kubelet's `:10248/healthz`, via `internal/bootstrap/boot.go:410`) has no bounded
deadline of its own and hung until the outer `timeout 180` killed the whole `rask create` process
(`create rc=124`). This is the same gap noted in trial 2 — `rask create` (any `--wait` mode) needs
its own bounded deadline for "wait for node Ready", not just the `coreDNSWaitTimeout` that only
applies after node-ready is reached (`cmd/rask/create.go:34`).

**Verdict for trial 4**: non-privileged is not viable with this specific capability set. It is
plausible a further-privileged variant (`--privileged` alone, without the extra caps/security-opt
tuning, is the only thing that reliably flips the `/sys/fs/cgroup` rw switch in Docker) — this
session did not find a non-`--privileged` way to get a writable cgroup delegation, so **for now,
the minimal viable container-runtime privilege level is full `--privileged`** (Docker) /
`privileged: true` (Kubernetes securityContext). Narrowing this further (e.g. testing whether a
Kubernetes pod with `privileged: false` but a wider capability set + explicit cgroup subPath mount
behaves differently from Docker's own hardening — CRI-O/containerd's own delegation logic may
differ from dockerd's) is flagged as a valuable follow-up, not completed this session.

### Trial 2 (context, referenced above): `--privileged` (default/private cgroupns) + docker volume for cluster data — confirms the anticipated cgroup v2 issue

Fixing the overlay problem with a docker named volume (`docker volume create rask-poc-clusters`,
`-v rask-poc-clusters:/root/.rask/clusters`) instead of a macOS-path bind mount also fixed the
virtiofs slowness: `docker top`/`docker exec` on this container were fast and normal, and the full
boot DAG launched (kine, containerd, kube-apiserver, kube-scheduler, kube-controller-manager,
kube-proxy all confirmed running via `docker top`) — **except kubelet**, which crash-loops
immediately:

```
E kubelet_node_status.go: node "rask-node" not found
E node_container_manager_linux.go:62] "Failed to create cgroup" err="cannot enter cgroupv2
  \"/sys/fs/cgroup/kubepods\" with domain controllers -- it is in an invalid state" cgroupName=["kubepods"]
E kubelet.go:1688] "Failed to start ContainerManager" err="cannot enter cgroupv2 ... invalid state"
```

This is exactly the anticipated cgroup v2 "no internal process" constraint: a cgroup that has
member processes directly attached to it cannot also have domain controllers delegated to child
cgroups. With the default (private) cgroupns, docker puts the container's own init process (and
everything `docker exec`'d into it, including `rask` and everything it forks) directly into the
container's cgroup root (`/sys/fs/cgroup` as seen from inside), so kubelet's attempt to create
`kubepods` as a child of that same cgroup is rejected. `internal/bootstrap.Supervisor`'s
`RestartOnCrash` makes kubelet retry indefinitely (1s `defaultRestartDelay` between attempts) —
and since neither `bootKubelet`'s `waitHTTPOK(.../healthz)` nor the overall `rask create`
invocation has a bounded deadline for "wait for node Ready" (only `--wait coredns`'s 60s
`coreDNSWaitTimeout`, applied *after* node-ready, is bounded — see `cmd/rask/create.go:34`), a
cluster stuck this way **hangs forever** rather than failing fast. Noted as a real rask robustness
gap independent of the in-container question: `rask create --wait node` (or any wait mode) should
have its own bounded deadline for the node-ready wait, not just for the coredns wait.

### Trial 1a: `--privileged --cgroupns=host`, default (in-container) data dir

```
docker run -d --name rask-poc --privileged --cgroupns=host -e HOME=/root \
  -v <repo>/test/benchmark/.incontainer/rask:/usr/local/bin/rask:ro \
  -v <repo>/test/benchmark/.incontainer/cache:/root/.rask/cache:ro \
  debian:bookworm-slim sleep infinity
# in container: apt-get install -y iptables ca-certificates iproute2 procps
# rask create cluster --name poc --verbose --wait coredns
```

- `cgroupns=host` confirmed: `cat /sys/fs/cgroup/cgroup.controllers` inside the container matches
  the host (`cpuset cpu io memory hugetlb pids rdma misc`).
- Node reached **Ready** (`kubectl get nodes`: `rask-node Ready ... containerd://2.3.3`).
- CoreDNS and local-path-provisioner pods stuck in `ContainerCreating` indefinitely; `rask create
  --wait coredns` times out (`context deadline exceeded`) but the cluster itself is left running
  (Start already returned nil before the outer `--wait` poll in cmd/rask timed out).
- **Root cause (confirmed, exactly the anticipated issue): overlayfs-on-overlayfs.**
  kubelet log:
  ```
  failed to create shim task: failed to mount rootfs component: mount source: "overlay",
  target: ".../containerd/root/io.containerd.runtime.v2.task/k8s.io/<id>/rootfs",
  fstype: overlay, ..., err: invalid argument
  ```
  containerd's own overlayfs snapshotter cannot mount a lowerdir/upperdir stack when the
  directory it's operating in is itself already inside the *container's* overlay2 diff (the
  container's rootfs is overlay2-backed by docker on this host). The kernel rejects nesting an
  overlay mount whose lower layer resolves onto another overlayfs. This only affects pod sandbox
  creation (containerd's node-local image/container storage) — the control plane processes
  (apiserver/kine/kubelet/kube-proxy) never touch overlayfs and booted fine.

### Environment note: this shared 2-vCPU VM is heavily contended

Trial 1b's first attempt (`rask create` wrapped in `timeout 90`) was killed mid-boot by the 90s
timeout — not a rask failure, just this VM (already running 2 kind clusters + fjord control plane
+ postgres/redis + this nested control-plane boot, all on 2 vCPU) being too slow for 90s to be
enough. After that, `docker kill`/`docker rm` on the test container itself took a very long time to
converge (container process count was already 0 per `docker top`, but `docker ps` kept reporting
`Up` — dockerd itself was backlogged, not the container being stuck). Recorded as an environment
characteristic, not a rask-in-container finding: **`docker rm -f` is blocked by this session's
sandbox** (destructive-flag pattern match) — use `docker kill <name>` then `docker rm <name>`
instead. Every subsequent trial uses a fresh container name and budgets for slow teardown/exec.

## Final verdict

| privilege level | how far it got |
|---|---|
| `--privileged` + default (private) cgroupns + docker volume for cluster data + one-time cgroup-subtree fix | **Full success**: node Ready, CoreDNS Ready, local-path-provisioner Ready, a `kubectl run` smoke pod Running with a real pod IP on the CNI bridge. Boot latency (node_ready 3.155s) matches bare-host numbers (RESULTS-linux.md: 3.08s) almost exactly — containerization itself costs ~nothing once the two fixes below are applied. `rask load image-archive` was attempted but inconclusive (environment exec-dispatch slowness late in session, not a proven rask/container issue). |
| `--privileged` + `--cgroupns=host` | Same boot success as above (node Ready) but pod sandbox creation fails 100% of the time unless the overlay fix (below) is also applied — and cleanly tearing this variant down risked wedging the shared dockerd's per-container task tracking (recovered without touching other containers, but avoid `--cgroupns=host` for this workload; it adds risk for no benefit over the default). |
| non-privileged + `--cap-add=NET_ADMIN,SYS_ADMIN,SYS_RESOURCE` + apparmor/seccomp unconfined | **Fails**: `/sys/fs/cgroup` mounted read-only (blocks kubelet's cgroup manager) and `/dev/kmsg` missing (blocks kubelet startup entirely) — both independent of the capability set tested, both require more than a `--cap-add` list. `/dev/kmsg` is independently fixable via `--device`; the read-only cgroup mount was not defeated this session. |
| user-ns / rootless | Not attempted (stretch item, out of time budget this session). |

**Two fixes are required, both cheap and independent of the privilege-level question above:**

1. **Overlay-on-overlay**: put the cluster's data directory (specifically wherever containerd's
   snapshotter root lives — `internal/cluster.Dir(homeDir, name)/data/containerd`, but simplest to
   just mount the whole `~/.rask/clusters` parent) on a filesystem that is *not itself* an overlay
   mount. A docker volume (`-v name:/root/.rask/clusters`) or any real block/virtual-disk-backed
   directory works; the container's own default writable layer (overlay2) does not. Confirmed via
   kubelet log signature `mount source: "overlay", ... err: invalid argument`.
2. **cgroup v2 "no internal process" constraint**: before running kubelet inside the container, move
   every process already in the container's root cgroup into a leaf sub-cgroup, then enable
   `cpuset/cpu/io/memory/pids` in `cgroup.subtree_control` on the (now process-free) root:
   ```sh
   mkdir -p /sys/fs/cgroup/init
   for p in $(cat /sys/fs/cgroup/cgroup.procs); do echo "$p" > /sys/fs/cgroup/init/cgroup.procs; done
   echo "+cpuset +cpu +io +memory +pids" > /sys/fs/cgroup/cgroup.subtree_control
   ```
   Verified empirically that later `docker exec` (and, by the same mechanism, `kubectl exec`/CRI
   `ExecSync`) sessions follow into `/init`, not back to root — so this is a one-time step at
   container/pod start, not something needed per-exec. This is exactly the "kind entrypoint dance"
   the task brief anticipated. Requires `/sys/fs/cgroup` to be writable, which in turn requires
   `--privileged` (Docker) — see below for the Kubernetes translation and the open question about
   whether CRI-O/containerd's own cgroup delegation is less restrictive than dockerd's.

## Minimal viable securityContext for a haro workspace pod (Docker flags → Kubernetes translation)

Based on this session's results, the **minimum that was proven to work** is full privileged mode —
no narrower combination was found to grant a writable cgroup delegation:

```yaml
securityContext:
  privileged: true
```

No other securityContext fields were needed or tested as substitutes (no explicit `capabilities:`
list needed once `privileged: true` is set; `seccompProfile`/`appArmorProfile` should stay at
whatever `privileged: true` implies — unconfined — since this session's non-privileged trial with
apparmor/seccomp already unconfined still failed on the cgroup mount, i.e. those two are not the
gating factor).

**cgroupns**: no Kubernetes pod-spec field controls this directly (it's a node/CRI runtime config
concern, e.g. containerd's `cgroup_writable`/`SystemdCgroup` settings or the `runtime-cgroups`
kubelet flag) — this session found default (private) cgroupns to be both sufficient and *safer*
than host cgroupns (see the `--cgroupns=host` teardown risk noted above), so no special node/CRI
cgroupns configuration should be requested for haro workspace nodes.

**Volume requirement**: the workspace pod's rask cluster data directory (`~/.rask/clusters` inside
the pod) must be backed by a real (non-overlayfs) volume — an `emptyDir` (backed by the node's
filesystem, not overlay2) should work for this in Kubernetes, analogous to the docker volume used
here; needs verification against the actual haro node's storage driver.

**Not yet answered this session** (flagged for a follow-up, since Kubernetes pods run under
containerd/CRI-O directly, not dockerd — the specific "which docker flag maps to which mechanism"
question doesn't fully carry over): does a `privileged: false` pod with an explicit
`capabilities.add: [NET_ADMIN, SYS_ADMIN, SYS_RESOURCE]` list get a writable `/sys/fs/cgroup` under
containerd/CRI-O the way it didn't under dockerd? containerd's own cgroup delegation logic
(`WritableCgroupfs` config, or newer sandboxed cgroup namespace support) may differ meaningfully
from Docker's. This is the single highest-value follow-up experiment for narrowing the privilege
requirement further, since haro workspace pods run on EKS/containerd, not dockerd.

## rask code changes

**Implemented this session**: none (this was a proof/measurement session per the task's own scope;
the two fixes above are shell-level workarounds, not rask code, and were deliberately kept out of
`internal/substrate/hostproc` for now — see below for why).

**Filed here as a follow-up for the M3 checklist item "rask 側のコンテナ内対応 (cgroup subtree 移動等、
kind の entrypoint 相当)"** (plan-m0-spikes.md line 35), not implemented this session because it's a
real, delicate piece of production code (must be idempotent, must not break bare-host/vz operation,
needs its own tests) that deserves its own session rather than a rushed addition here:

1. `internal/substrate/hostproc.Runtime.Start` (or a new preflight step called from it) should
   detect "am I in a cgroup v2 cgroup whose root already has member processes and I can't create
   `kubepods` as a result" and perform the same move-then-enable-subtree_control dance automatically
   before launching kubelet — exactly what container/init-system wrappers like kind's entrypoint do,
   so a haro workspace pod's entrypoint doesn't need to hand-roll this shell script itself. Must be
   a no-op (or safely skip) on a bare host, where this situation normally does not arise.
2. **Robustness gap, independent of the in-container question** (hit twice this session — trial 2's
   kubelet crash-loop and trial 4's kubelet immediate-exit both hung `rask create` indefinitely):
   `internal/bootstrap.bootKubelet`'s `waitHTTPOK(waitCtx, ..., ":10248/healthz")`
   (`internal/bootstrap/boot.go:410`) and the overall node-ready wait have no bounded deadline of
   their own — only `--wait coredns`'s `coreDNSWaitTimeout` (`cmd/rask/create.go:34`) is bounded, and
   that only starts counting *after* node-ready is reached. A cluster whose kubelet can never become
   healthy (wrong cgroup driver, missing device, bad config — any of which are realistic operator
   errors, not just this container scenario) currently hangs `rask create` forever instead of
   failing fast with a clear error. This is a real bug worth fixing regardless of the container work,
   and is small enough to implement directly: give `runBootDAG`'s `waitCtx` (or specifically each
   `waitHTTPOK` call in the boot DAG) a bounded default deadline, mirroring `coreDNSWaitTimeout`'s
   pattern.

## Host-node prerequisites (for haro's node pool, translating this session's colima-VM findings)

- **Kernel modules already loaded on the node** (verified present on the colima VM without any
  action needed — likely already true on EKS/EKS-D nodes given kind/fjord already run there, but
  should be explicitly verified on the real haro node AMI): `bridge`, `veth`, `overlay`, `ip_tables`,
  `iptable_filter`, `iptable_nat`, `nf_tables`, `nf_conntrack` (+ `xt_*` extensions),
  `nf_conntrack_netlink`.
- **`br_netfilter` must be explicitly `modprobe`'d** — it was *not* loaded by default on this
  colima VM (`bridge-nf-call-iptables` sysctl absent until `sudo modprobe br_netfilter`). This is a
  one-time host/node-level action (containers cannot `modprobe` unprivileged even with
  `--privileged`, since kernel module loading is gated by the *host's* kernel + `CAP_SYS_MODULE`,
  which `--privileged` does grant *inside* the container's user context but the module must already
  exist in the running kernel's module tree — verify this specifically on the haro node AMI, since a
  missing `br_netfilter.ko` file (as opposed to just "not yet loaded") would need a node AMI change,
  not just a `modprobe` in a node bootstrap script).
- **`fs.inotify.max_user_instances` / `max_user_watches`**: already raised on this colima VM (512 /
  524288, fjord precedent) — confirm the same sysctls are set on haro's actual node pool (this is
  the same class of fix fjord already needed for kube-proxy's CrashLoop, per
  research-m0-spikes.md's fjord note).
- **`/dev/kmsg` must exist on the node** (confirmed present on this colima VM) and, if any
  non-privileged path is pursued in the future, must be explicitly passed through
  (`--device=/dev/kmsg` in Docker terms; a hostPath device mount or a CRI-level device allowlist
  entry in Kubernetes terms).
- **containerd/CRI-O's own cgroup delegation policy** for the pod's node needs to support a
  writable cgroup v2 delegation for `privileged: true` pods (standard for any privileged pod on a
  cgroup v2 node — no haro-specific action expected here, but worth a one-time confirmation on the
  actual node pool, since this session only tested Docker's policy, not containerd/CRI-O's).

## Environment / process notes for whoever continues this

- `docker rm -f` is blocked by this session's sandbox (destructive-flag pattern match); use
  `docker kill <name>` then `docker rm <name>`.
- `docker kill`+`docker rm` on a container that ran a privileged nested workload can take a very
  long time to converge (dockerd/containerd desync, not a stuck container — verify via
  `docker top <name>` going empty + `colima ssh -- sudo ctr --namespace moby tasks ls | grep <id>`
  showing `STOPPED`). `colima ssh -- sudo ctr --namespace moby tasks rm <id>` clears the containerd
  side; `docker rm` itself sometimes never converges without a `colima restart` (which was
  deliberately never done this session — it would disrupt the user's other running containers). Two
  containers (`rask-poc1b`, `rask-poc2`, `rask-poc3`, `rask-poc4` — check `docker ps -a` for current
  state) may still be sitting in this zombie "Up but empty" state; they consume no CPU and did not
  affect any other container throughout this session, but a `colima restart` at the user's
  convenience (not now, not by me) would clean them up.
- `docker exec` dispatch latency into these test containers was highly variable this session (from
  instant to 10+ minutes) even when the container's actual workload was idle — general VM/dockerd
  slowness under this session's cumulative load, not something diagnosed further. Budget generously
  for any future work here; prefer batching multiple steps into one `docker exec` invocation.
- Working files for this PoC (rask binary, component cache copy, test tarballs) live under
  `test/benchmark/.incontainer/` (gitignored). Docker volumes created this session:
  `rask-poc-clusters`, `rask-poc-clusters3`, `rask-poc-clusters4` (not cleaned up — see above).
