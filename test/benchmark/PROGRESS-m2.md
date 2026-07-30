# M2: haro operator on rask (Linux/colima) — progress tracker

Not committed; scratch state for this session. Updated after every
meaningful checkpoint. See plan-m0-spikes.md's M2 section and
research-m0-spikes.md's "haro 要件" section for the task's origin.

## Exit criteria (from task spec)

1. Workspace/WorkspaceProfile CRDs apply cleanly.
2. harooperator binary (linux/arm64, out-of-cluster, --leader-elect=false)
   reconciles workspaceprofile.smoketest.yaml + workspace-sample.yaml: pod
   Running, PVC Bound.
3. TokenReview path: projected SA token with audience "haro" validates via
   TokenReview against rask's apiserver; apiserver accepts the custom
   audience via a configurable flag (not hardcoded).
4. Gateway API + external-snapshotter CRDs installable via a rask addon
   mechanism (CRDs only, no controllers).
5. E2E wall-clock + friction recorded in RESULTS-m2.md.

## Done

- [x] Read plan-m0-spikes.md, research-m0-spikes.md, PROGRESS.md (M1
      incident/corrective-action history), haro hack/local/README.md.
- [x] Confirmed colima VM healthy, other containers (fjord-lb, flagfield,
      haro-local, postgres-haro, redis) untouched before starting.
- [x] internal/bootstrap: added `apiAudiences(issuer, extra)` pure helper
      (config.go, unit tested) and `Config.ExtraAPIAudiences []string`
      threaded into kube-apiserver's `--api-audiences` flag (boot.go).
- [x] internal/substrate: `Runtime.Start` interface widened to
      `Start(ctx, name, opts StartOptions)` with `StartOptions.ExtraAPIAudiences`.
      Updated both implementations: hostproc (wires opts into
      bootstrap.Config) and vz (accepts and currently ignores opts — vz's
      guest-side boot path doesn't yet plumb this through; documented
      in-code, not silently dropped).
- [x] cmd/rask/create.go: new repeatable `--api-audience` flag ->
      `substrate.StartOptions{ExtraAPIAudiences: ...}`. Tests added
      (create_test.go: flag passed through / omitted-defaults-to-empty).
- [x] internal/manifests: vendored Gateway API v1.5.1 "standard" channel
      CRD-only bundle (gatewayapi-crds.yaml, from the v1.5.1 GitHub release
      asset, matching sigs.k8s.io/gateway-api v1.5.1 in harooperator's
      go.mod) + `ApplyGatewayAPICRDs`. Vendored external-snapshotter v8.6.0
      CRDs (snapshot-crds.yaml: VolumeSnapshotClass/Content/VolumeSnapshot
      only, VolumeGroupSnapshot excluded — harooperator's scheme doesn't
      register it) + `ApplySnapshotCRDs`. No dedicated unit test added for
      either (matches existing localpath.go precedent in this exact
      package: thin go:embed + ApplyYAML wrappers rely on ApplyYAML's own
      generic tests + real-cluster E2E apply for CRD-schema correctness).
- [x] Repo-wide re-verification after all of the above: `go build`/`go vet`
      clean on darwin/arm64 (host) AND linux/arm64 (cross-compile),
      golangci-lint 0 issues, `go test -race -shuffle=on -count=1` green on
      darwin. Cross-compiled internal/substrate/hostproc's test binary for
      linux/arm64 and ran it live inside colima: all 17 tests still pass
      after the Start() signature change (no -race — same known gap as M1,
      no cross-arch C toolchain for CGO).

## Done (cont.) — full E2E, all 4 technical exit criteria PASS

- [x] StorageClass "standard" compat alias added to
      internal/manifests/local-path-storage.yaml (second StorageClass doc,
      same rancher.io/local-path provisioner, not marked default).
- [x] cmd/rask/addon.go: `rask addon install gateway-api|snapshot-crds`,
      wired into root.go. Vendored Gateway API v1.5.1 standard-channel CRDs
      and external-snapshotter v8.6.0 CRDs (versions matched to
      harooperator's own go.mod).
- [x] Built harooperator linux/arm64 from the READ-ONLY layerone checkout
      (CGO_ENABLED=0), ran out-of-cluster in colima with --leader-elect=false
      against a real rask cluster's admin kubeconfig.
- [x] Applied haro CRDs + workspaceprofile.smoketest.yaml + workspace-sample.yaml.
      PVC Bound (~15s), pod 3/3 Running (~30s total). Operator logs clean,
      Workspace status Provisioned=True/Suspended=False.
- [x] TokenReview verified: read the real workspace pod's projected token
      (audience "haro") directly off the host filesystem (kubectl
      exec/logs hit an unrelated pre-existing rask gap — see RESULTS-m2.md
      §2 friction note), POSTed to /apis/authentication.k8s.io/v1/tokenreviews
      with spec.audiences: [haro] -> status.authenticated: true, correct
      identity (system:serviceaccount:haro-user-smoketest:ws-smoketest).
- [x] Cleaned up: killed harooperator by exact PID, `rask delete cluster`,
      removed temp /etc/hosts entry, moved scratch binaries out of the repo.
      Verified other colima containers (fjord-lb, flagfield, haro-local,
      postgres-haro, redis) had continuous uptime throughout — untouched.
- [x] RESULTS-m2.md written with full wall-clock breakdown + friction notes
      + follow-ups for M3.

## Exit criteria verdict: 4/4 technical criteria PASS (details in RESULTS-m2.md)

## Safety notes carried over from M1 (see PROGRESS.md for full incident)

- No broad `pkill -f` on this shared VM — ever. Cleanup only via
  `rask delete cluster` or PIDs sourced from rask's own state.json.
- One thing at a time on this 2 vCPU VM; no restarting/resizing colima; no
  macOS vz VM launches (frozen per user instruction, see rask-project
  memory).
