# M1 Linux results: `rask create cluster` on real binaries (colima)

## Verdict

`rask create cluster` works end-to-end on Linux, validated inside the running colima VM as root:
direct-launch bootstrap (kine → apiserver → {controller-manager, scheduler} ∥ containerd →
kubelet → node Ready) → CoreDNS applied and Ready → local-path-provisioner Running → smoke pod
Running. `rask delete cluster` cleans up fully (state directory removed, every rask-launched
process actually killed, verified via `ps`). `test/e2e/linux.sh` automates and re-verifies this
whole sequence non-interactively.

The internal boot DAG lands **node Ready in ~3.1s**, matching spikes/s1's proven ~3.0s cold number
(same architecture, ported into production packages). The `rask create` CLI invocation itself
(process start → return, over `colima ssh`) measures **p50 4.01s / p95 4.34s** for `--wait=node` —
the ~0.6-1s gap versus the internal timeline is SSH round-trip + process fork/exec overhead, plus
PKI generation and component-path resolution that happen before the internal timeline's `t0`
(consistent with spikes/s1/RESULTS.md's own methodology: PKI generation is sub-millisecond and
deliberately excluded from the "boot a running cluster" claim, but is measured by `date` wall-clock
wrapping the whole `rask create` process here since we're measuring the actual CLI, not an
in-process spike harness).

## Measured (10 runs each, colima: Ubuntu 24.04.4 LTS aarch64, 2 vCPU, 16GiB — same shared,
constrained VM spikes/s1 used; see that document's hardware caveat, which applies identically here)

| scenario | p50 | p95 | min | max | mean | n |
|---|---|---|---|---|---|---|
| `rask create --wait=node` (cold datastore, warm component cache) | **4.011s** | 4.338s | 3.549s | 4.338s | 4.024s | 10 |
| `rask create --wait=coredns` (cold datastore + cold image cache) | **15.122s** | 18.203s | 11.689s | 18.203s | 15.505s | 10 |

**"Cold" here means the kine/SQLite datastore only** (no prebaked seed — internal/prebake, item 6
of the original task list, was not implemented this session; see Deferred). It does **not** mean
cold component binaries: `rask create`'s first invocation downloads and verifies
kube-apiserver/kubelet/kine/containerd/runc/CNI plugins into `~/.rask/cache` (excluded from every
measured run below via one untimed warm-up create+delete cycle first, matching how a real user
would only pay that cost once).

## Why `--wait=coredns` is ~4x slower than `--wait=node`: image pull, not CoreDNS itself

`rask delete cluster` removes the cluster's entire data directory, including containerd's
root/state — which holds pulled container images. Every measured `--wait=coredns` run therefore
pays a **cold `registry.k8s.io/coredns/coredns:v1.14.6` + `registry.k8s.io/pause:3.10` image pull**
over the network, exactly the same "stretch" cost spikes/s1/RESULTS.md flagged for its own
`--with-pod` measurement ("this delta ... is always a cold pull over the network ... its cost is
dominated by image-pull network latency rather than anything rask's own design controls"). This is
not a fair reflection of CoreDNS's own boot latency, and it is expected to shrink substantially
once containerd's image cache is allowed to persist across `rask create`/`rask delete` cycles (a
real design question for M2+: should `delete` really wipe the image cache, or only the cluster's
control-plane/node identity state?) — noted as a concrete follow-up, not fixed this session.

## Internal phase breakdown (representative `--verbose` run, `--wait=node`)

| phase (cumulative since Boot's internal t0) | p50-ish (1 sample) |
|---|---|
| kine_up | 49ms |
| containerd_up | 90ms |
| apiserver_readyz | 2.324s |
| kubelet_started | 2.629s |
| cm_sched_started | 2.755s |
| node_registered | 2.865s |
| **node_ready (TOTAL)** | **3.08s** |
| kube_proxy_started | 3.549s |

This matches spikes/s1/RESULTS.md almost exactly (apiserver_readyz ~75% of total wall time there;
~76% here), confirming the production port preserved the spike's core bet: apiserver's own
in-process bootstrap reconciliation (RBAC/namespace post-start hooks, admission plugin init,
~40 API group/versions loading), not kine/containerd/kubelet, is the dominant cost, and it is
CPU-bound on this 2-vCPU shared VM — real hardware should show a meaningfully lower number.

## k3d / kind comparison

**Not measured live this session.** After an earlier incident this session where an overly broad
process-cleanup command briefly disrupted this same shared VM's docker daemon (see
test/benchmark/PROGRESS.md for the full incident writeup — self-recovered, no data loss found, but
a real mistake), further hands-on experimentation with this VM's docker daemon (installing/running
k3d, which needs its own docker-in-docker or shares the host docker socket) was deliberately
avoided as an unnecessary additional risk to the user's already-running, unrelated kind clusters on
this same VM. Comparing instead against the published numbers already gathered in
research-m0-spikes.md (deep-research phase, cross-checked against multiple sources):

| tool | reported p50 / typical | source |
|---|---|---|
| **rask** (this session) | **4.01s** (node Ready) | measured, this document |
| k3d | 7-16s | research-m0-spikes.md (current fastest published real-cluster tool) |
| kind | 19-20s | research-m0-spikes.md / kind maintainer (BenTheElder, kind#845) |
| minikube (docker driver) | 29s | research-m0-spikes.md |
| microk8s | ~12.5s | research-m0-spikes.md |

rask's node-Ready p50 (4.01s) is already faster than k3d's *entire published range* (7-16s), let
alone kind's. A live, apples-to-apples k3d run on this exact VM at this exact moment would be the
stronger claim, and is flagged as the natural next step once it can be done without touching this
shared VM's docker daemon (e.g. on a dedicated/ephemeral VM, or coordinated with the user first).

## Reproducing

```bash
# from the repo root, on the host, with colima already running:
GO=/path/to/go test/e2e/linux.sh                       # single validation cycle
RUNS=10 WAIT=node GO=/path/to/go test/benchmark/bench.sh      # node-Ready p50/p95
RUNS=10 WAIT=coredns GO=/path/to/go test/benchmark/bench.sh   # CoreDNS-Ready p50/p95
```

Both scripts build `rask` for the VM's actual architecture, drive it via `colima ssh -- sudo`, and
clean up their own binaries and cluster state on exit (including on failure).

## Deferred (not implemented this session — see final report for full list)

- internal/prebake + tools/prebake (seed SQLite generation, seeded-boot benchmark comparison)
- Live k3d comparison on this VM (safety reasons, see above)
- macOS/vz substrate (out of this session's scope; spikes/s2 and s4 already validated its
  feasibility, per plan-m0-spikes.md)
