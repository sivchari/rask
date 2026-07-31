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

## M3-prep: prebaked seed (`internal/prebake`), cold vs seeded `--wait=coredns`

`internal/prebake` (seed SQLite generation, `rask seed build`, seeded-boot auto-detection in
`rask create`) is now implemented — see `test/benchmark/PROGRESS-m3prep.md` for the design
(seed key = k8s version + manifest-bundle digest) and `internal/bootstrap`/`internal/substrate`
changes. Measured cold vs seeded `rask create cluster --wait coredns` in colima, both scenarios
using the same warm component-cache, cold-image-cache methodology as the M1 numbers above (every
run's `rask delete` wipes the cluster's containerd root, so both groups pay a fresh
CoreDNS/local-path-provisioner image pull identically — this is unrelated to what prebaking
controls, see "Why `--wait=coredns` is ~4x slower" above; the seed only shaves apiserver's own
bootstrap reconciliation and the CoreDNS/local-path-provisioner *API apply* round trip, not the
image pull itself).

### Reference value (n=10 each) — contaminated by a concurrent macOS vz E2E run

**Do not treat as this feature's true baseline.** A second agent's `rask __vm-host` macOS vz VM
(`Virtualization.framework` XPC process, observed at 20-40% host CPU throughout) was running
concurrently on this same physical host for the entire measurement, competing for the same cores
colima's own 2-vCPU guest is scheduled on. Recorded for completeness only, not as the headline
number:

| scenario | p50 | p95 | mean | min | max | n |
|---|---|---|---|---|---|---|
| cold (no seed) | 19.177s | 26.653s | 19.660s | 12.590s | 30.695s | 10 |
| seeded | 12.592s | 15.805s | 12.492s | 8.939s | 15.953s | 10 |

Even under this contamination, seeded was consistently faster (mean **36.5%** lower, p50 **34.3%**
lower) — a real signal despite the noise, not just within-noise variance, but the absolute numbers
above should not be quoted as "what prebaking gets you" since host contention inflated every value
(both cold's 15.1s p50 baseline from the M1 section above, measured on an otherwise-idle host, and
this run's own min/max spread, are both consistent with that read).

### Re-measurement (n=3 each) — load average recorded, contamination still partially present

The concurrent vz VM (PID 73577 on the host) could not be stopped (a different, in-progress agent
session's work, not something this session is authorized to interrupt), so this re-measurement is
**best-effort, not fully clean** — included for transparency, not as a clean-room number. Host and
guest load average recorded immediately before and after this run:

| when | host (macOS, 14-core) load avg (1/5/15m) | colima guest (2 vCPU) load avg (1/5/15m) | vz XPC process CPU% observed |
|---|---|---|---|
| before | 4.20 / 6.57 / 6.64 | 0.44 / 0.83 / 0.78 | 22.6% |
| after | 5.21 / 6.09 / 6.42 | 0.38 / 0.67 / 0.72 | 30.7% |

| scenario | mean | median | min | max | n |
|---|---|---|---|---|---|
| cold (no seed) | 15.498s | 15.597s | 15.269s | 15.628s | 3 |
| seeded | 11.916s | 10.971s | 9.790s | 14.988s | 3 |

n=3 is too small for a meaningful p95, and the guest's own load average (0.4-0.8 on 2 vCPUs) looks
fine in isolation — the contamination is a host-level scheduling effect (macOS scheduling colima's
vCPU threads against the concurrent vz VM's threads on the same physical cores), not something
visible from inside the guest. Directionally consistent with the n=10 reference (seeded faster,
similar ~23% mean reduction here vs ~36% there), but neither run should be read as "the" number for
apiserver's ~550ms `apiserver_readyz` saving spikes/s1/RESULTS.md found in isolation — that
component-level number remains the more trustworthy claim about what seeding itself does; these
`--wait=coredns` wall-clock numbers are dominated by contention and cold image-pull noise on this
shared, constrained VM. A clean re-run once the host is genuinely idle is the natural follow-up.

## Deferred (not implemented this session — see final report for full list)

- Live k3d comparison on this VM (safety reasons, see above)
- macOS/vz substrate work for `internal/prebake` (seed building has no vz equivalent yet — a vz
  cluster's datastore lives inside its guest VM's own disk, not on a host-readable path; see
  `cmd/rask/seed_darwin.go`)
- A genuinely clean-host cold-vs-seeded re-measurement (both runs in this session had a concurrent
  macOS vz VM competing for host CPU; see above)

## Image-bundle: prefetching CoreDNS/local-path-provisioner/pause into `~/.rask/cache/images`

`internal/imagebundle` (pure-Go image prefetch via `google/go-containerregistry`'s `crane`, no
daemon) plus `internal/substrate/hostproc`'s `Create`/`Start` wiring — see
`test/benchmark/PROGRESS-imagebundle.md` for the full design and code pointers. Summary: `Create`
prefetches CoreDNS/local-path-provisioner/busybox (the helper-pod image)/`registry.k8s.io/pause`
into `~/.rask/cache/images/<arch>/`, concurrently with the existing component-binary download, as
docker-save tar archives (best-effort — a prefetch failure never fails `Create`, matching
`--component-dir`'s existing "no hard network dependency" contract). `Start` then imports whatever
is cached into the cluster's containerd concurrently with `bootstrap.Boot` (not serially after it),
so the images are already present in containerd's `k8s.io` namespace before
`ApplyCoreDNS`/`ApplyLocalPathProvisioner` even runs.

Same host as this file's earlier M1/M3-prep sections (contended, shared with several unrelated
long-running docker containers on this colima instance — see load average below; **not** a
clean-room host, same caveat as the M3-prep section above). Both binaries were cross-compiled
`GOOS=linux GOARCH=arm64` and run via `colima ssh -- sudo <bin> create/delete cluster --name bench`,
matching `bench.sh`'s own methodology. "before" = this branch immediately prior to this change
(verified via `git stash`); "after" = with the change. Each run's `delete` removes the cluster's own
containerd root (so every run pays a fresh image *import*, "before" pays a fresh registry *pull*),
but does **not** touch `~/.rask/cache` (component cache or, now, the image cache) — matching every
prior measurement in this file.

Host and guest load average immediately before this measurement: host (macOS, shared) 6.25 / 7.59 /
7.48 (1/5/15m), colima guest (2 vCPU) 1.16 / 1.61 / 1.25.

### `--wait=coredns` (n=5 each, warm image cache for "after")

| scenario | p50 | mean | min | max | n |
|---|---|---|---|---|---|
| before (registry pull every run) | 15.278s | 14.879s | 12.611s | 15.946s | 5 |
| after (cached images imported from local disk) | 10.558s | 10.592s | 7.606s | 12.690s | 5 |

p50 improved **30.9%** (15.278s → 10.558s), mean **28.8%**. Not a full elimination of the gap to
`--wait=node`'s ~2.4s: the remaining ~8s is CoreDNS's own container start + readiness-probe latency
and `waitForCoreDNS`'s poll interval, not image pull — see the phase table below. Both series were
measured on the same contended host as each other (back-to-back, same session), so the comparison
is fair even though neither is a clean-room number in isolation.

### `--wait=coredns`, cold image cache (first-ever create, n=1)

Same "after" binary, but `~/.rask/cache/images` wiped first (component-binary cache left warm — this
isolates the image cache specifically, not "a cold `~/.rask` in general"): **14.853s**. Close to
"before"'s own p50 (15.278s) — expected, since a cold image cache means `Create`'s prefetch pays the
same network cost the old code always paid, just moved earlier (into `Create`, concurrently with the
component-binary download) instead of happening implicitly during `Start`'s manifest-driven pull. The
speedup only shows up from the *second* create onward, once the image cache is warm.

### `--wait=node` (n=5 each) — confirms no regression

| scenario | p50 | mean | min | max | n |
|---|---|---|---|---|---|
| before | 2.364s | 2.525s | 2.303s | 3.142s | 5 |
| after | 2.518s | 2.489s | 2.368s | 2.566s | 5 |

Within run-to-run noise (both series' own min/max spread is larger than the 0.15s p50 delta between
them) — confirms the concurrent image-import goroutine does not add to the node-Ready critical path,
as intended (Start does not wait extra for it beyond what `bootstrap.Boot` itself already takes).

### Where the remaining `--wait=coredns` time goes (representative `--verbose` run, "after", warm cache)

| phase (cumulative since Boot's internal t0) | value |
|---|---|
| kine_up | 25ms |
| containerd_up | 45ms |
| apiserver_readyz | 1.672s |
| node_registered | 1.682s |
| **node_ready** | **1.682s** |
| kube_proxy_started | 1.778s |
| kubelet_started | 1.953s |
| cm_sched_started | 2.000s |

This is `bootstrap.Boot`'s own internal timeline (guest/process-internal, not wall-clock from the
host CLI) — it ends at node Ready, ~2s here. It does not cover `--wait=coredns`'s own remaining
budget (`ApplyCoreDNS`'s API round trip, CoreDNS's pod being scheduled, its container starting, and
`waitForCoreDNS`'s readiness poll), which is not currently broken out into its own timeline phase —
a reasonable follow-up if a more precise breakdown is wanted. The measured total ("after" p50
10.558s) minus this ~2s internal boot minus the host-side CLI/SSH-round-trip overhead documented
earlier in this file leaves several seconds in that CoreDNS-specific tail, consistent with container
start + readiness-probe latency rather than a registry pull (the whole point of this change).
