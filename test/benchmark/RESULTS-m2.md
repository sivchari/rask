# M2 results: haro operator (harooperator) against a rask cluster (colima, Linux)

## Verdict

All 4 technical exit criteria from the task spec pass, validated live against a real
`rask create cluster` (hostproc substrate) inside the shared colima VM, running the real
harooperator binary (built from the READ-ONLY layerone checkout) out-of-cluster.

| # | Criterion | Result |
|---|---|---|
| 1 | Workspace/WorkspaceProfile CRDs apply cleanly | PASS |
| 2 | harooperator reconciles workspaceprofile.smoketest.yaml + workspace-sample.yaml: pod Running, PVC Bound | PASS |
| 3 | TokenReview: projected SA token (audience "haro") validates against rask's apiserver | PASS |
| 4 | Gateway API + external-snapshotter CRDs installable via a rask addon mechanism | PASS |
| 5 | E2E wall-clock + friction recorded | this document |

## 1. CRDs apply cleanly

```
kubectl apply -f harooperator/config/crd/bases/haro.layerx.co.jp_workspaceprofiles.yaml
kubectl apply -f harooperator/config/crd/bases/haro.layerx.co.jp_workspaces.yaml
```
Both created without error against a freshly created rask cluster (`kube-apiserver` already
running Node,RBAC authorization; CRD Established happens synchronously fast enough that no
"no matches for kind" race was observed applying the CR a few seconds later — consistent with
haro's own README note about why CRD-then-CR must be split into separate applies, which this
session's manual sequencing already respects).

## 2. Operator reconcile: pod Running, PVC Bound

Build (read-only against the layerone checkout, output only ever written outside the repo):
```
cd layerone/go/services/devplatform/v1/harooperator
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o harooperator ./cmd
```
Run out-of-cluster inside colima, against the rask cluster's own admin kubeconfig
(`system:masters`, so no extra RBAC binding was needed for this operator to reconcile):
```
sudo ./harooperator --leader-elect=false \
  --workspace-bootstrap-url=http://127.0.0.1:9007 \
  --health-probe-bind-address=:18081
```
(`--workspace-bootstrap-url` is a required flag but does not need to be reachable for this
smoketest path — see haro's own README: workspaceprofile.smoketest.yaml exists precisely to
validate PVC/Deployment/Service assembly without a working phone-home target.)

Applied `workspaceprofile.smoketest.yaml` (public nginx:alpine + redis images,
`storageClassName: standard`) + `workspace-sample.yaml`. Timeline from `kubectl apply` to
steady state (10 polls, 3s apart):

| t (approx) | PVC | Pod |
|---|---|---|
| +0s | Pending | Pending (containers not yet created) |
| +15s | **Bound** | ContainerCreating |
| +30s | Bound | **3/3 Running** |

Final Workspace status: `phase: Bootstrapping`, `Provisioned=True`, `Suspended=False`,
`Bootstrapped=False (AwaitingPhoneHome)` — expected, since `--workspace-bootstrap-url` points
nowhere reachable in this session; the exit criterion (pod Running, PVC Bound) does not require
a working phone-home target and both were met.

**Friction found**: `kubectl exec`/`logs` against a pod on a rask cluster fails with
`unable to upgrade connection: Unauthorized`. Root cause: rask's kube-apiserver is not
configured with `--kubelet-client-certificate`/`--kubelet-client-key`, so it has no credential
to authenticate to kubelet's exec/logs streaming server when proxying (kubelet's own
`authentication.x509.clientCAFile` requires a client cert; anonymous auth is off). This is a
real, general rask gap (not haro-specific) — kubectl port-forward likely has the same issue —
worth a follow-up milestone: mint an apiserver-to-kubelet client cert in `internal/bootstrap/pki.go`
(same pattern already used for the controller-manager/scheduler loopback certs) and wire it into
`bootDatastoreAndControlPlane`'s apiserver args. Worked around for this session by reading the
projected token file directly off the host filesystem instead (see §3) — hostproc's v1 design
has no container isolation, so kubelet's per-pod volume directories
(`data/kubelet/root/pods/<uid>/volumes/...`) are real, host-readable files; this only works
because of that specific v1 property and would not work against a real containerized/VM
substrate (vz), where exec/logs must go through the proper apiserver→kubelet path — reinforcing
that the kubelet client cert gap should be fixed before M3, not left as a permanent workaround.

## 3. TokenReview validates the "haro" audience

Added a repeatable `--api-audience` flag to `rask create cluster` (wired through a new
`substrate.StartOptions` on the `Runtime.Start` interface, not hardcoded to "haro" — any
TokenReview client can request its own audience this way). Cluster created with
`--api-audience haro`; the running apiserver process confirmed:
```
--api-audiences=https://kubernetes.default.svc.cluster.local,haro
```

Read the real workspace pod's own projected token (audience "haro", the exact one
`workspace_children.go`'s `bootstrapTokenAudience` constant produces) directly off the host
filesystem (see friction note above for why not via `kubectl exec`), and posted it to
`/apis/authentication.k8s.io/v1/tokenreviews`:

```
status:
  audiences: [haro]
  authenticated: true
  user:
    username: system:serviceaccount:haro-user-smoketest:ws-smoketest
    groups: [system:serviceaccounts, system:serviceaccounts:haro-user-smoketest, system:authenticated]
```

Confirms the full path: kubelet requests a token for audience "haro" from the apiserver's
TokenRequest API -> apiserver mints it (issuer `https://kubernetes.default.svc.cluster.local`) ->
a TokenReview presenting that token with `spec.audiences: [haro]` authenticates successfully,
because "haro" is in `--api-audiences`. Without `--api-audience haro` at create time, this
TokenReview would fail authentication (audience mismatch) — not re-verified as a negative
control this session (the apiserver command-line flag inspection above is direct enough
evidence of the wiring), but worth doing explicitly in a follow-up if this becomes a regression
concern.

## 4. Addon CRDs installable

New `rask addon install <name>` command (`cmd/rask/addon.go`), CRD-only, no controllers:

```
rask addon install gateway-api    # sigs.k8s.io/gateway-api v1.5.1 "standard" channel, vendored
rask addon install snapshot-crds  # kubernetes-csi/external-snapshotter v8.6.0 client CRDs, vendored
```

Both versions were chosen to match exactly what harooperator's own `go.mod` imports
(`sigs.k8s.io/gateway-api v1.5.1`, `github.com/kubernetes-csi/external-snapshotter/client/v8 v8.6.0`),
so the installed CRD schemas are guaranteed compatible with the typed objects the operator
builds against them.

Measured (already-warm apiserver, single run): `gateway-api` 0.41s, `snapshot-crds` 0.09s.
Verified via `kubectl get crd`: all 8 Gateway API CRDs
(gatewayclasses/gateways/httproutes/grpcroutes/referencegrants/tlsroutes/backendtlspolicies/listenersets)
and all 3 snapshot CRDs (volumesnapshotclasses/volumesnapshotcontents/volumesnapshots) present.
`groupsnapshot.storage.k8s.io` CRDs deliberately excluded — harooperator's scheme only registers
`volumesnapshot/v1`, not `VolumeGroupSnapshot`.

## 5. E2E wall-clock summary

| step | wall time |
|---|---|
| `rask create cluster --wait coredns --api-audience haro` (warm component cache) | 17.2s |
| `rask addon install gateway-api` | 0.41s |
| `rask addon install snapshot-crds` | 0.09s |
| haro CRDs apply (2 CRDs) | <1s |
| harooperator startup to steady reconcile loop | <1s |
| WorkspaceProfile + Workspace apply -> PVC Bound | ~15s (first image pull for local-path-provisioner's helper pod + PVC WaitForFirstConsumer binding) |
| -> pod 3/3 Running | ~30s total (nginx:alpine + redis:7.4-bookworm + envoy sidecar image pulls, cold on this cluster) |
| TokenReview round trip | <1s |

Total wall clock from `rask create` to a fully Running, Bound haro smoketest workspace with a
validated TokenReview: **~50s**, entirely inside the constrained 2 vCPU shared colima VM
alongside 3 already-running kind clusters + postgres + redis (all confirmed untouched
afterward — uptime continuous, no restarts).

## rask-side changes made

- `internal/bootstrap/config.go` / `boot.go`: `apiAudiences()` helper + `Config.ExtraAPIAudiences`,
  threaded into kube-apiserver's `--api-audiences` flag.
- `internal/substrate/substrate.go`: `Runtime.Start` interface gained a `StartOptions` parameter
  (`ExtraAPIAudiences`); updated both implementations (`hostproc` wires it through, `vz` accepts
  and documents that it doesn't plumb it through yet — out of scope, macOS/vz is a separate
  milestone).
- `cmd/rask/create.go`: new repeatable `--api-audience` flag.
- `internal/manifests/local-path-storage.yaml`: added a second `standard`-named StorageClass
  alias (same `rancher.io/local-path` provisioner) so haro's hardcoded
  `storageClassName: standard` binds without editing haro's own manifests.
- `internal/manifests/gatewayapi.go` + `gatewayapi-crds.yaml`, `snapshot.go` + `snapshot-crds.yaml`:
  vendored CRD-only bundles (see §4 for exact versions/sources).
- `cmd/rask/addon.go` (+ `root.go` wiring): `rask addon install <gateway-api|snapshot-crds>`.
- Tests added throughout (TDD): `internal/bootstrap/config_test.go` (`apiAudiences`),
  `cmd/rask/create_test.go` (flag plumbing), `cmd/rask/addon_test.go` (dispatch/validation).
  `go build`/`go vet` clean on darwin/arm64 and linux/arm64, `golangci-lint` 0 issues,
  `go test -race -shuffle=on -count=1` green on darwin; cross-compiled
  `internal/substrate/hostproc`'s test binary and re-ran all 17 tests live on real Linux
  (colima) after the `Start()` signature change — still green.

## What remains for M3

- Fix the kubelet client-cert gap found in §2 (`kubectl exec`/`logs`/`port-forward` currently
  Unauthorized against any rask cluster) — needed for a real haro dev loop (debugging inside a
  workspace pod), not just for this session's TokenReview workaround.
- Wire `Config.ExtraAPIAudiences` through the vz (macOS) substrate's guest-side boot path, once
  vz work resumes (currently frozen per user instruction — see rask-project memory).
- `internal/prebake` (seed SQLite) — still not implemented (carried over from M1's Deferred list),
  would shrink the ~17s `rask create --wait coredns` number.
- M3's original goal (amd64 ECR workspace images via Rosetta) is already known-blocked per
  research-m0-spikes.md's 2026-07-30 addendum (Rosetta support withdrawn after a host-crash
  incident) — this session's smoketest deliberately used arm64-friendly public images instead,
  consistent with that decision.
- A live k3d/kind comparison for the M2 flow specifically (this document only measured rask)
  remains open, same reasoning as RESULTS-linux.md's M1 deferral (avoid extra risk to this
  shared VM's already-running kind clusters).
