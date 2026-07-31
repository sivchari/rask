# M1 macOS results: `rask create cluster` on real Apple Silicon hardware (vz)

## Verdict

`rask create cluster` works end-to-end on macOS via `Virtualization.framework` (no Docker, no
Linux VM): a real vz VM boots, `cmd/rask-init` runs as PID 1 (module load → tmpfs switch_root →
data disk format/mount → static networking), direct-launch bootstrap (kine → apiserver →
{controller-manager, scheduler} ∥ containerd → kubelet → node Ready) → CoreDNS applied and Ready
→ local-path-provisioner Running → a smoke pod reaches Running. `rask delete cluster` cleans up
fully (vm-host process and its VM both actually terminated, cluster state directory removed,
verified via `ps` after every single run in this session — never just trusted the exit code).

Fourteen live E2E attempts (`e2e1`-`e2e14`) were needed to find and fix fourteen distinct bugs
before reaching this state — see `PROGRESS-darwin.md`'s "M1 macOS: complete — full bug list, in
discovery order" section for the complete list with root causes. None of them were guessed and
left unverified; all were root-caused against real evidence (kernel boot logs, guest component
logs fetched live through the guest agent's file-read endpoint, real upstream Kubernetes/
containerd source, real Alpine package contents).

## Measured (10 runs each, real hardware: Apple M4 Pro, 14 cores (10P+4E), 48GiB, macOS 26.5.2)

| scenario | p50 | p95 | min | max | mean | n |
|---|---|---|---|---|---|---|
| `rask create --wait=node` (cold datastore, warm component cache) | **4.629s** | 4.694s | 4.533s | 4.694s | 4.632s | 10 |
| `rask create --wait=coredns` (cold datastore + cold image cache) | **17.649s** | 20.743s | 15.509s | 20.743s | 18.361s | 10 |

"Cold" here means the same thing as the Linux M1 measurement's definition (`RESULTS-linux.md`):
only the kine/SQLite datastore and containerd's image cache are cold per run (`rask delete` wipes
both), not the component binaries — those are downloaded/verified into `~/.rask/cache` once and
reused across every run measured below (one untimed `rask create`/`delete` cycle, plus the 14
prior debugging attempts, already primed this cache before these numbers were taken).

Both series ran back-to-back via a small driver script (`rask delete` before and after each
timed `rask create`, wall-clock measured around the `rask create` subprocess only), 10 successful
runs each, zero failures, zero leftover `vm-host` processes or cluster state directories after
any run (`ps aux` and `~/.rask/clusters/` checked after each series).

### Host load during measurement (not a clean-room number — see caveat below)

| when | host (macOS, 14-core) load avg (1/5/15m) | colima vz XPC process CPU% |
|---|---|---|
| before `--wait=node` series | 3.23 / 3.24 / 3.07 | n/a |
| after `--wait=node` series | 3.15 / 3.22 / 3.07 | n/a |
| before `--wait=coredns` series | 3.23 / 3.24 / 3.08 | 28.3% |
| after `--wait=coredns` series | 2.62 / 3.04 / 3.04 | 35.4% |

**Caveat, following the exact same honest-disclosure convention `RESULTS-linux.md` already
established for this identical situation**: a separate, in-progress agent session was actively
running its own `rask create`/`delete` cycles inside a colima-hosted Linux container (`rask-poc`,
via `docker exec`) for the *entire* duration of both series above, and the user's own
long-running colima VM (`Virtualization.framework` XPC process, ~28-40% host CPU throughout,
independently confirmed via `lsof` to hold colima's own disk images — not a rask process) was
present the whole time as well. Neither could be stopped: the other agent's work was not
something this session is authorized to interrupt, and the colima VM is the user's own persistent
background workload, not something either agent controls. On a 14-core/48GiB host with `--wait=
node`'s numbers landing in a tight 4.5-4.7s band (161ms spread across 10 runs) despite this
contention, the practical impact looks small for this scenario; `--wait=coredns`'s wider spread
(15.5-20.7s) is more plausibly load-sensitive (cold image pull over the network competing for the
same host resources), so its absolute numbers should be read with that caveat in mind — the
*relative* ordering and rough magnitude (network image pull dominating over CoreDNS's own boot
cost, same as the Linux measurement found) is still a reliable signal.

## Why `--wait=coredns` is ~4x slower than `--wait=node`: image pull, not CoreDNS itself

Same root cause as the Linux M1 measurement: `rask delete cluster` removes the cluster's entire
data directory, including containerd's root/state — which holds pulled container images. Every
measured `--wait=coredns` run therefore pays a cold `registry.k8s.io/coredns/coredns:v1.14.6` +
`registry.k8s.io/local-path-provisioner` image pull over the network. A single live pull observed
during ad-hoc verification (not part of the timed series) completed in 3.4s — consistent with
network/registry latency, not anything rask's own boot path controls, dominating the gap.

## Internal phase breakdown (representative `--verbose` run, `--wait=node`, attempt e2e14)

| phase (cumulative since Boot's internal t0) | value |
|---|---|
| containerd_up | 106ms |
| kine_up | 128ms |
| apiserver_readyz | 2.353s |
| kubelet_started | 2.588s |
| cm_sched_started | 2.614s |
| node_registered | 2.722s |
| **node_ready (TOTAL)** | **3.029s** |
| kube_proxy_started | 3.476s |

This is the guest's own internal timeline (kernel boot start to node Ready, ~3.0s), measured from
inside the VM via `internal/bootstrap`'s phase markers. The gap versus the host-side CLI
measurement above (4.629s p50) is `Virtualization.framework` VM startup overhead (kernel/EFI-ZBOOT
unwrap, device model init, gvisor-tap-vsock network bring-up) plus PKI generation and
component-path resolution that happen before the guest's own `t0` — the same accounting
`RESULTS-linux.md` uses for its SSH-round-trip gap, adapted for vz's VM-startup-instead-of-SSH
overhead. Matches the earlier live boot timelines observed this session (attempts 7, 9, 13, 14 all
landed in the 3.0-4.3s guest-internal range).

## k3d / kind comparison

**Not measured live this session** (would require Docker on this Mac, out of scope for a
Docker-free vz substrate and not installed here). Comparing against the same published numbers
`RESULTS-linux.md` already gathered (research-m0-spikes.md, cross-checked against multiple
sources):

| tool | reported p50 / typical | source |
|---|---|---|
| **rask (vz, this document)** | **4.629s** (node Ready) | measured, this document |
| **rask (Linux hostproc, colima)** | **4.011s** (node Ready) | `RESULTS-linux.md` |
| k3d | 7-16s | research-m0-spikes.md |
| kind | 19-20s | research-m0-spikes.md / kind maintainer (BenTheElder, kind#845) |
| minikube (docker driver) | 29s | research-m0-spikes.md |
| microk8s | ~12.5s | research-m0-spikes.md |

rask's macOS vz node-Ready p50 (4.629s) is faster than k3d's entire published range (7-16s) and
kind's, on real Apple Silicon hardware with no Docker Desktop, no Linux VM the user has to manage
themselves, and while directly competing with another active VM workload for host CPU.

## Reproducing

```bash
# from the repo root, on macOS (Apple Silicon), with the entitled+codesigned binary built:
make build && make codesign
./rask create cluster --name smoke --verbose --wait coredns
kubectl --kubeconfig ~/.rask/clusters/smoke/kubeconfig run smoke --image=registry.k8s.io/pause:3.10 --restart=Never
./rask delete cluster --name smoke
```

No script is checked in for the 10-run benchmark itself (a throwaway Python driver was used and
discarded — see this session's process for the exact create/delete/measure loop, mirroring
`test/benchmark/bench.sh`'s Linux methodology).

## Deferred (not implemented this session)

- `internal/prebake` (seed building) has no vz equivalent yet — a vz cluster's datastore lives
  inside the guest VM's own disk, not on a host-readable path (same gap already noted in
  `RESULTS-linux.md`'s Deferred section).
- A genuinely clean-host (no concurrent colima/other-agent activity) re-measurement, once the
  host is idle — the caveat above should not be read as invalidating the numbers, but a clean
  re-run would tighten the `--wait=coredns` spread specifically.
- Rosetta / amd64 image execution: deliberately disabled this session after a suspected-related
  host crash (see `PROGRESS-darwin.md`'s "Policy changes" section) — every measurement above uses
  arm64 images only.
