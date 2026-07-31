# Handoff: addon CRDs move to fjord

`rask addon install <name>` (CRD-only, no controllers) has been removed from rask. rask follows
kind's "plain cluster, bring your own manifests" philosophy; installing EKS-emulation-specific
CRDs (Gateway API, external-snapshotter) is fjord's job as the EKS-parity layer on top of rask,
not rask's.

## Removed

- `cmd/rask/addon.go`, `cmd/rask/addon_test.go`, and the `newAddonCommand` wiring in
  `cmd/rask/root.go`.
- `internal/manifests/gatewayapi.go` + `internal/manifests/gatewayapi-crds.yaml`.
- `internal/manifests/snapshot.go` + `internal/manifests/snapshot-crds.yaml`.

## Versions to vendor on fjord's side

These were pinned to match the versions the haro operator (rask's dogfooding target) imports in
its own `go.mod`, so the installed CRD schemas are compatible with the typed objects the operator
builds against them. Same pinning rationale applies to fjord.

- **Gateway API**: `sigs.k8s.io/gateway-api v1.5.1`, "standard" channel, CRD-only install manifest:
  `https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml`
  (GatewayClass/Gateway/HTTPRoute/GRPCRoute/ReferenceGrant/TLSRoute/BackendTLSPolicy/ListenerSet
  — 8 CRDs).
- **external-snapshotter**: `github.com/kubernetes-csi/external-snapshotter/client/v8 v8.6.0`,
  CRDs vendored from
  `https://github.com/kubernetes-csi/external-snapshotter/tree/v8.6.0/client/config/crd`
  (VolumeSnapshotClass/VolumeSnapshotContent/VolumeSnapshot only — the `groupsnapshot.storage.k8s.io`
  CRDs in that same directory were deliberately excluded, since haro's scheme only registers
  `volumesnapshot/v1`, not `VolumeGroupSnapshot`; re-evaluate that exclusion independently on
  fjord's side if fjord's own scheme differs).

Source: `test/benchmark/PROGRESS-m3prep.md`, `test/benchmark/RESULTS-m2.md` §4.

## Notes for fjord

- `rask.internal/manifests.ApplyYAML` is the pattern that was used to apply these (parse a
  multi-document YAML manifest, apply each object via a dynamic client + RESTMapper) — not
  reusable directly since it was package-internal, but a fine reference implementation to port.
- `internal/manifests.BundleDigest` (rask's seed-cache invalidation key) never included the
  gateway-api/snapshot CRDs — they were opt-in, not part of every cluster's default Start bundle
  — so this removal does not change the digest or invalidate existing rask seeds.
