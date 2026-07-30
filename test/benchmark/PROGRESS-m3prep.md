# M3 prep: kubelet exec/logs/port-forward fix + prebake seed — progress tracker

Not committed; scratch state for this session. Two tasks from the M2
follow-up list in RESULTS-m2.md ("What remains for M3").

## Task 1: apiserver -> kubelet exec/logs/port-forward (Unauthorized fix)

### Root cause (confirmed, two distinct bugs, both fixed)

1. apiserver had no `--kubelet-client-certificate`/`--kubelet-client-key`,
   so kubelet's x509 authenticator rejected every apiserver->kubelet
   request outright ("Unauthorized") — this was the bug RESULTS-m2.md
   originally found.
2. Once (1) is fixed, a second, previously-undiscovered bug surfaces:
   apiserver's default `--kubelet-preferred-address-types` order tries the
   Node's Hostname address first (`rask-node`, cluster.NodeName), which has
   no DNS record anywhere rask runs. Every exec/logs/port-forward request
   failed name resolution ("dial tcp: lookup rask-node ... no such host")
   before ever presenting the new client cert.

### Fix

- `internal/pki/leaf.go`: no change (IssueClient already generic).
- `internal/bootstrap/pki.go`: `generatePKI` now also issues
  `apiserver-kubelet-client.{crt,key}` (CN `kube-apiserver-kubelet-client`,
  **no Organization** — deliberately not kubeadm's `O=system:masters`
  convention, documented inline at the issuance site: this cert is only
  ever presented outbound by apiserver to kubelet, never used to
  authenticate an inbound request to apiserver itself, and kubelet's own
  `authorization.mode` here is `AlwaysAllow` (unchanged — see below), which
  grants any authenticated identity regardless of group. `system:masters`
  would add zero function today while creating a second de facto
  cluster-admin credential on disk for no benefit. If kubelet's
  `authorization.mode` ever moves to `Webhook`, a dedicated RBAC binding
  for whatever group this cert carries would need to be added at the same
  time — noted in the code comment, not implemented (not needed: kubelet's
  built-in RBAC bootstrap policy already covers the webhook SAR path for
  the `system:node:*` identities if that migration happens later; this
  cert is unrelated to that).
- `internal/bootstrap/boot.go`: apiserver args gained
  `--kubelet-client-certificate`, `--kubelet-client-key`, and
  `--kubelet-preferred-address-types=InternalIP,Hostname` (kind uses the
  same InternalIP-first convention for the same reason).
- kubelet's `authentication.x509.clientCAFile` was already set to the
  cluster CA (`internal/bootstrap/config.go`'s `writeKubeletConfig`, from
  before this session) — no change needed there.
- kubelet's `authorization.mode` was already `AlwaysAllow` — deliberately
  left unchanged. It already authorizes any authenticated identity
  regardless of group, so switching to `Webhook` (kubeadm's actual
  convention) would be an unrelated security-hardening change, not
  something required to close this specific gap — see
  `internal/bootstrap/pki.go`'s issuance-site comment for the full
  reasoning. Not done, to avoid unrequested scope creep.

### Verification

Unit tests: `internal/bootstrap/pki_test.go` — new
`TestGeneratePKI_KubeletClientCertIsNarrowlyScopedAndVerifies` (CN, empty
Organization, verifies against the generated CA, valid PEM key). All of
`go build`/`go vet`/`go test -race -shuffle=on -count=1 ./...` clean on
darwin/arm64; `go build`/`go vet` clean cross-compiled linux/arm64
(`golangci-lint` also clean for every package this session touched — the
handful of `errcheck` findings in `internal/substrate/vz` are pre-existing,
from a separate uncommitted work-in-progress already present in this
working tree before this session started, unrelated to either task here;
left untouched).

Live E2E in colima (hostproc substrate), 2026-07-30:

```
rask create cluster --name t1verify --wait coredns --verbose
# node_ready in 2.889s (comparable to prior M2 numbers, no regression)

kubectl run smoke4 --image=busybox:1.36 ... (a real listening pod, see below)

kubectl exec smoke4 -- echo "exec works: hello"   # -> "exec works: hello", exit 0
kubectl logs smoke4                               # -> real container stdout, exit 0
kubectl port-forward pod/smoke4 18080:8080 &
curl http://127.0.0.1:18080                        # -> "pong", exit 0
```

All three (exec, logs, port-forward) confirmed working end-to-end against a
real rask cluster for the first time. Two busybox `nc` fixture false
failures along the way were test-script bugs (BusyBox `nc` doesn't support
`-4`; had to bind explicitly with `-s 127.0.0.1` instead), not rask bugs —
both times the actual error surfaced was already past authentication (a
"connection refused"/"connection reset" from *inside* the pod netns, which
only happens once apiserver has successfully authenticated to kubelet and
kubelet has set up the stream into the container).

Cleanup: `rask delete cluster --name t1verify`, verified `cni0` bridge
removed and cluster state directory gone. See "Known gap" below for a
teardown issue found (and worked around) along the way.

### Known gap found (out of scope, not fixed this session): orphaned containerd-shim processes survive `rask delete`

Every `rask create` + workload-pod + `rask delete` cycle in this session
left 2-4 `containerd-shim-runc-v2` processes running, still referencing the
now-deleted cluster's containerd socket path
(`/root/.rask/clusters/<name>/data/containerd/containerd.sock`). Root
cause (not investigated further): `internal/substrate/hostproc/teardown.go`'s
`Stop` SIGTERMs then SIGKILLs containerd directly, but never asks
containerd to gracefully shut down its shims first (`shim delete` per
running task) — SIGKILLing the parent daemon leaves each container's shim
process orphaned (this is expected containerd behavior: shims are
deliberately designed to survive a containerd daemon restart). This
predates this session (same orphaned-shim processes, from an unrelated
`haro-e2e` cluster, were already present in colima when this session
started) and is unrelated to either of this session's two tasks, so it was
**not fixed** — only worked around by killing the exact orphaned PIDs
after each test run (never a pattern-based kill, per this session's
environment rules). Worth a dedicated follow-up: `hostproc.Stop` should
either call containerd's own shutdown path before SIGKILLing it, or
explicitly reap known shim PIDs (containerd exposes a
`ctr namespaces` / task list to enumerate them) as part of teardown.

## Task 2: prebake seed

### Design: seed key

`internal/prebake.Key(k8sVersion) = "<k8sVersion>-<manifests.BundleDigest()>"`.

- **Included**: Kubernetes version (different apiserver/controller-manager
  builds can bootstrap different built-in RBAC/API-group content into the
  datastore) and a sha256 digest over the exact CoreDNS + local-path-provisioner
  objects the default manifest bundle applies
  (`internal/manifests/bundle.go`'s `BundleDigest`, `json.Marshal`-ing each
  typed CoreDNS object plus the embedded local-path-storage.yaml bytes).
  If either changes, the digest changes, and old seeds simply stop matching
  (never need explicit invalidation/versioning).
- **Deliberately excluded**: `--api-audience` (an apiserver command-line
  flag, not stored datastore content — has zero bearing on whether a seed's
  captured objects are still valid) and cluster name/PKI (regenerated
  fresh every create regardless of seed use; cluster identity — node name,
  service/pod CIDRs — is fixed across every rask cluster by design, see
  `internal/cluster`'s package doc, independent of prebaking).

Seed files live at `homeDir/seeds/<Key>.db` (`internal/prebake.Path`),
i.e. `~/.rask/seeds/<k8sVersion>-<digest>.db` in production.

### Design: build + auto-use

- `internal/substrate/hostproc/seed.go` (new, linux-only):
  `(*Runtime).BuildSeed(ctx, buildName, outPath)` boots a real throwaway
  cluster via the runtime's own `Create`/`Start` (not seeded itself — a
  seed can't bootstrap from a seed of unknown provenance), waits for both
  default-bundle Deployments (CoreDNS, local-path-provisioner) to report a
  Ready replica (`internal/manifests.WaitDeploymentReady`, new, extracted
  from `cmd/rask/create.go`'s `--wait coredns` logic so both call sites
  share one poller), `Stop`s it cleanly (so kine checkpoints its SQLite WAL
  — capturing mid-write would risk a torn database), copies
  `SeedSourcePath` (new accessor, `dataDir/kine/state.db`) to `outPath`,
  then `Delete`s the throwaway cluster's state. Any failure after `Create`
  triggers best-effort `Stop`+`Delete` before returning, mirroring `Start`'s
  own failure-cleanup contract.
- `cmd/rask/seed_linux.go` / `seed_darwin.go`: `rask seed build` (darwin
  returns `nil` — a vz cluster's datastore lives inside its guest VM's own
  disk, not on a host-readable path, so seed building has no vz
  implementation yet, same reasoning already documented for
  `Config.ExtraAPIAudiences` not reaching vz). Guards against a leftover
  throwaway cluster from a previously-killed build with a clear error
  before delegating to `BuildSeed`.
- `internal/substrate.StartOptions` gained `SeedPath` (additive, mirrors
  the existing `ExtraAPIAudiences` field/precedent from M2).
  `hostproc.Runtime.Start` threads it into `bootstrap.Config.SeedPath`
  (already existed, unused until now — `Boot` already called
  `Datastore.SeedFrom` when set) and **skips** the CoreDNS/local-path-provisioner
  apply round trip entirely when a seed was used (the seeded datastore
  already contains those objects; `ApplyYAML`/`ApplyCoreDNS` are idempotent
  no-ops on re-apply either way, so this is a pure latency optimization,
  not a correctness requirement).
- `cmd/rask/create.go`: `createCluster` auto-detects a matching seed via
  `prebake.Path(homeDir, components.DefaultK8sVersion)` + a file-exists
  check, with **no flag** — seeding is a pure boot-time optimization a
  substrate implementation applies transparently, never a behavior change
  a caller opts into.

### Unit tests added

`internal/manifests/bundle_test.go` (digest determinism +
sensitivity-to-content-change via a package-var swap),
`internal/manifests/wait_test.go` (`WaitDeploymentReady` against a fake
clientset: ready/timeout/missing-deployment), `internal/prebake/key_test.go`
(`Key`/`Path` composition), `internal/substrate/hostproc/hostproc_test.go`
(new `SeedSourcePath` case), `cmd/rask/create_test.go` (seed
auto-detection present/absent via `fakeRuntime`). `BuildSeed` itself is
**not** unit-tested (needs real root + real component binaries, same as
`hostproc.Start` — this repo's established precedent per RESULTS-m2.md:
thin orchestration wrappers rely on their callees' own unit tests plus a
real E2E run, not a mocked integration test). Verified instead by the live
measurement below.

### E2E: seed build + create/delete cycle

`rask seed build` run live in colima: created `~/.rask/seeds/v1.33.13-a5fea...db`
(323KB) in ~19s wall clock, then correctly tore the throwaway
`__rask-seed-build__` cluster down. First run of `BuildSeed` found and fixed
a real bug: `Runtime.Delete` only removes `dataDir` (PKI/datastore/containerd/kubelet
state), not the cluster's top-level directory (kubeconfig, state.json) —
`cmd/rask/delete.go`'s `deleteCluster` already does an extra
`os.RemoveAll(cluster.Dir(...))` after `rt.Delete` for exactly this reason,
which `BuildSeed`'s first draft had missed, leaving
`clusters/__rask-seed-build__/` behind after every build. Fixed in
`internal/substrate/hostproc/seed.go` by adding the same extra removal
(both on the success path and the failure-cleanup `defer`).

Separately (same underlying gap as Task 1's "known gap" above, already
present before this session): every `rask create` + workload-pod +
`rask delete` cycle — including the ones `BuildSeed` itself runs — leaves
2-4 orphaned `containerd-shim-runc-v2` processes behind, cleaned up each
time by exact PID (never a broad pattern kill) as part of this session's
own hygiene, per the environment rules.

### E2E measurement: 10x cold vs 10x seeded `rask create --wait coredns`

Run once (n=10 each), then flagged by the coordinator as contaminated by a
concurrent macOS vz E2E run from a separate, unstoppable agent session on
the same host; re-run smaller (n=3 each) with host/guest load average
recorded before and after. **Both results, with the contamination caveat
and load-average data, are now in `RESULTS-linux.md`'s new "M3-prep:
prebaked seed" section** — not duplicated here. Headline: seeded was
consistently faster in both runs (mean ~36.5% lower at n=10, ~23% lower at
n=3), a real signal despite the contamination noise, but neither run's
absolute numbers should be quoted as a clean baseline; a re-run on a
genuinely idle host is the natural follow-up.
