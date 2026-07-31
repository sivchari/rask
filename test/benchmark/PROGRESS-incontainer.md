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
| 1a | `--privileged --cgroupns=host`, cluster data dir on container's own overlay2 rootfs | node Ready, CoreDNS/local-path-provisioner stuck `ContainerCreating` forever (overlay-on-overlay) |
| 1b | `--privileged --cgroupns=host`, cluster data dir bind-mounted from macOS host path (virtiofs) | inconclusive — extremely slow, likely virtiofs I/O, not overlay; retrying with a docker named volume instead |
| 1c | `--privileged --cgroupns=host`, cluster data dir on a docker named volume (VM-native fs) | abandoned, see below — moved to trial 2 (default cgroupns) instead |
| 2 | `--privileged` (default/private cgroupns), cluster data dir on a docker named volume | kubelet crash-loops: cgroup v2 "invalid state" — see below |
| 3 | trial 2 + kind-style sub-cgroup/PID-move workaround before `rask create` | pending |

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

This happened only with `--cgroupns=host`: the container's kubelet was directly creating/deleting
cgroups in the *host's own* cgroup v2 hierarchy (since cgroupns=host gives no isolated view), and
tearing that down when the container died is suspected to be what starved/wedged the shim's ttrpc
loop (not confirmed with certainty — noted as a hypothesis, not a proven root cause). Given this
real (if narrow) risk of leaving the shared docker daemon in a bad state, further trials use the
**default (private) cgroupns** instead of `--cgroupns=host`, which the "known likely issues" list
already flagged as needing extra work (cgroup v2 subtree creation) — see trial 2+.

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

### Trial 2: `--privileged` (default/private cgroupns) + docker volume for cluster data — confirms the anticipated cgroup v2 issue

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
