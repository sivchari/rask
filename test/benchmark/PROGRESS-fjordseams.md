# fjord-integration seams + pkg/cluster library API — progress tracker

Not committed; scratch state for this session. Branch: m0-spikes. Implements
the 4 fjord-integration seams requested (component-dir override, arbitrary
apiserver args, preboot files, CoreDNS image override) plus a follow-on
requirement added mid-session by the coordinator: a stable-leaning,
importable `pkg/cluster` Go library API, with cmd/rask rewired to use it.

## Design implemented

### 1. Component source override (`--component-dir`)

`internal/components/source.go` (new): a `ComponentSource` interface —
`Resolve(ctx, k8sVersion, arch) (*Paths, error)` — with two implementations:

- `DownloadCacheSource` (default): thin wrapper over the existing
  `Cache.Ensure`.
- `LocalDirSource`: overlays `kube-apiserver`/`kube-controller-manager`/
  `kube-scheduler`/`kubelet`/`kubectl` from a local directory, validated
  present+executable up front (`validate` collects and reports *every*
  missing/bad binary in one error, not just the first). `kube-proxy` is
  deliberately excluded from the override set — rask always runs it as a
  host process (see `bootKubeProxy`), while EKS ships it as a containerized
  addon, so fjord's EKS-D extraction has no host kube-proxy binary to hand
  rask anyway. Every non-k8s component (kine/containerd/runc/CNI) always
  comes from the wrapped `Cache`.

`internal/components/ensure.go` refactored: `Cache.Ensure` split into
`ensureK8sBinaries(bins []string)` + `ensureNonK8sInto` + a new
`ensureNonOverridden` (kube-proxy + non-k8s only), reused by
`LocalDirSource.Resolve`.

`substrate.StartOptions.ComponentDir string` (empty = default cache) is the
seam — deliberately a plain string, not the `ComponentSource` interface
itself, so it stays trivially serializable/comparable across the
substrate boundary and doesn't force every `StartOptions` consumer to
depend on `internal/components`. Each substrate builds the actual
`ComponentSource` internally (`hostproc.componentSource`).

**Interface change**: `substrate.Runtime.Create` gained a
`StartOptions` parameter (previously `Create(ctx, name) error`). Necessary
because `Create` is what pre-warms the component cache — if it always
downloaded the default k8s binaries regardless of `ComponentDir`, a
`--component-dir` override in a network-restricted environment (fjord's
whole use case) could still hard-fail on an unwanted download, defeating
the seam entirely. Every implementation (hostproc, vz) and call site
(cmd/rask/create.go, hostproc's own `BuildSeed`, both fake_runtime_test.go
files) updated.

### 2. Arbitrary apiserver flags (`--apiserver-arg key=value`, repeatable)

`substrate.StartOptions.ExtraAPIServerArgs []string` → `bootstrap.Config.
ExtraAPIServerArgs` → `internal/bootstrap/config.go`'s new
`buildAPIServerArgs(base, extra)`.

**Collision semantics (decision): error, not last-wins.** kubeadm's own
`extraArgs` lets a caller override anything kubeadm generates, because
kubeadm has no downstream code depending on the *exact* resolved value of
its own flags. rask does: the readyz probe URL is built from the same
`apiserverPort` constant passed via `--secure-port`, TLS trust throughout
`Boot` assumes `--tls-cert-file`/`--client-ca-file` are exactly what was
passed, `--anonymous-auth=false`/`--authorization-mode=Node,RBAC` are
security invariants. A caller-supplied key matching one of rask's own
managed flags (extracted from the `base` slice by key, not hardcoded
separately — DRY) is rejected with a clear error instead of silently lost
to kube-apiserver's own later-flag-wins parsing. `--api-audience` stays a
separate, additive mechanism (kept, not migrated to `--apiserver-arg`, no
breaking change) — and, as a side effect of the collision check, a caller
who tries `--apiserver-arg api-audiences=...` directly gets a clear error
pointing at `--api-audience` instead, since `api-audiences` is itself a
rask-managed flag key.

Tested: `internal/bootstrap/config_test.go` (append order, collision
rejection with and without a leading `--`, missing-`=` rejection, empty
extra-args no-op).

### 3. Pre-boot files (`--preboot-file src=dest`, repeatable)

`substrate.StartOptions.PrebootFiles []PrebootFile` (`{Src, Dest string}`).
New `substrate.PrebootSubdir = "preboot"` constant and
`substrate.StagePrebootFiles(dataDir, files) error` (shared helper, used by
both substrates), with path-traversal validation on `Dest` (real
external-input boundary — `Dest` is caller-controlled and interpolated
into a filesystem path).

**Convention**: absolute destination = `filepath.Join(dataDir, "preboot",
Dest)`. For hostproc, `dataDir` = `<homeDir>/clusters/<name>/data`
(computable by a caller in advance from a chosen cluster name — e.g.
`~/.rask/clusters/<name>/data/preboot/<dest>` for the default home dir),
making the dest path referenceable from an `--apiserver-arg` value in the
*same* `rask create` invocation, which is exactly fjord's intended usage
(`--preboot-file webhook.yaml=auth/webhook.yaml --apiserver-arg
authentication-token-webhook-config-file=.../preboot/auth/webhook.yaml`).

- **hostproc**: `StagePrebootFiles` called in `Start`, before
  `bootstrap.Boot` — real support, this is fjord's actual target substrate.
- **vz**: also implemented (not stubbed) — the seam existed in the
  initramfs builder as claimed. `Start` stages files host-side under
  `dataDir/preboot` (same convention); the separate `RunVMHost` process
  (`vmhost.go`) reads that staged directory back via new
  `internal/substrate/vz/preboot.go`'s `buildPrebootCpio` (builds a small
  per-cluster cpio placing each file at `guestlayout.PrebootDir` — new
  guest-layout constant) and `concatInitramfs` (byte-concatenates it onto
  the shared, host-cached template initramfs — cpio's `newc` format
  supports concatenated archives; the kernel's initramfs unpacker keeps
  scanning for another header after each `TRAILER!!!` entry rather than
  stopping at EOF, per `cpio.go`'s existing doc comment). Skipped
  (reverts to the shared template path unmodified) when nothing was
  staged — the common case pays zero extra cost. Verified via unit tests
  only (`preboot_test.go`) — NOT run through a real vz VM boot this
  session; see "vz E2E" below for why.

### 4. CoreDNS image override (`--coredns-image`)

`manifests.ApplyCoreDNS(ctx, clientset, image)` and `coreDNSDeployment(image)`
now take the image explicitly (was the `CoreDNSImage` package const
directly). `manifests.BundleDigest(coreDNSImage string)` now incorporates
it — **required** so a prebaked seed built for one CoreDNS image is never
matched by a create request for a different one (`prebake.Key`/`prebake.
Path` gained a `coreDNSImage` parameter accordingly; `rask seed build` and
`cmd/rask/create.go` both pass either the flag value or the resolved
default explicitly). `substrate.StartOptions.CoreDNSImage string` threads
through; empty means "use `manifests.CoreDNSImage`", resolved by each
substrate's own `applyManifests`/`coreDNSImage()` helper (hostproc) or the
package-level guest boot path (vz — always default, see below).

## vz (macOS) substrate: what's supported, what isn't, and why

`Start`'s existing doc comment already documented that `ExtraAPIAudiences`
and `SeedPath` are silently unsupported (an accepted, pre-existing gap: vz's
guest-side boot config travels over the kernel command line, not
`StartOptions`, and neither of those two is safety-relevant if ignored).
Extended for the 4 new seams:

- **PrebootFiles**: real support (see above).
- **ComponentDir**: hard error, not silent ignore. `Create` rejects it
  outright. Reason: vz's template initramfs is built once and cached
  *per host*, shared across every cluster — baking a component-dir
  override into it would either pollute that shared cache for the next
  (non-overridden) cluster, or require a whole second per-cluster
  initramfs-build pipeline analogous to the preboot cpio, which is not
  "trivially includable" the way preboot files were (preboot reuses the
  concatenation trick for a *tiny* overlay; a component override replaces
  multi-hundred-MB binaries that the template-caching model exists
  specifically to amortize across clusters).
- **ExtraAPIServerArgs**, **CoreDNSImage**: hard error, not silent ignore.
  Unlike the pre-existing `ExtraAPIAudiences`/`SeedPath` gap (worst case:
  a missing TokenReview audience, or a slower cold boot — both visible,
  neither silently wrong), silently dropping a caller-specified security-
  relevant apiserver flag or a requested container image would substitute
  rask's own default with no signal that happened. Erroring fast is safer
  and matches this codebase's existing "validate at the boundary, fail
  clearly" pattern (e.g. `LocalDirSource.validate`).

fjord's own target is Linux (EKS emulation is a Linux/hostproc concern per
`research-haro-cluster-infra.md`); vz is documented elsewhere in this repo
as "a parallel local-dev track", so this split is intentionally biased
toward full support on hostproc.

## pkg/cluster — importable Go library API (added mid-session, coordinator request)

`pkg/cluster` (chosen over `pkg/rask`, matching the explicit "same shape as
kind's `pkg/cluster`" instruction — no naming collision with
`internal/cluster` in practice: a file can import a same-named package
under its own different name without any alias, since a file's own package
clause doesn't occupy identifier space).

- `Provider` (concrete struct, not an interface — matches kind's own
  `pkg/cluster.Provider` shape): `Create(ctx, name, Options) (Result,
  error)`, `Delete(ctx, name) error`, `List() ([]string, error)`,
  `KubeConfigPath(name) string`, `KubeConfig(name, ExportOptions) ([]byte,
  error)`, `LoadImages(ctx, name, []ImageSource) error`.
- `NewProvider(homeDir string) (*Provider, error)`: the real public
  constructor — empty `homeDir` defaults to `~/.rask`; platform runtime
  selected internally via the same build-tag dispatch cmd/rask already uses
  (`runtime_linux.go`/`runtime_darwin.go`, literal copies of
  `cmd/rask/substrate_{linux,darwin}.go`'s `newPlatformRuntime`).
- `NewProviderWithRuntime(rt substrate.Runtime, homeDir string) *Provider`:
  exported (not unexported) *only* so cmd/rask (same module) can inject its
  existing `fakeRuntime` test double and keep every existing cmd/rask test
  working unchanged. Its parameter type lives in `internal/substrate`,
  which is not importable from outside this module — so despite being
  exported, it is not actually callable by an external consumer (verified,
  see "External-module import check" below). Documented as such.
- `Options` mirrors the 4 new seams (`ComponentDir`, `ExtraAPIServerArgs`,
  `PrebootFiles`, `CoreDNSImage`) plus the pre-existing ones
  (`ExtraAPIAudiences`, `Wait`). **Deviation from the literal request**:
  `HomeDir` is a `NewProvider` constructor parameter, not an `Options`
  field — it selects *which Runtime backend gets constructed at all*, so
  letting it vary per-`Create`-call on an already-constructed `Provider`
  doesn't make structural sense (mirrors how cmd/rask itself already fixes
  `homeDir` once, at `main()`, not per-subcommand). `ContextFormat`
  likewise lives on a separate `ExportOptions` (used by the new
  `KubeConfig` method) rather than `Options`, since it's a rendering-time
  concern for reading an *existing* cluster's kubeconfig, not a boot-time
  one — mirrors the CLI's own `create` vs. `export kubeconfig` command
  split. Both deviations are called out explicitly in `provider.go`'s doc
  comments, not silently done.
- `PrebootFile`, `ImageSource` mirror their `internal/substrate`
  equivalents field-for-field, so this package's exported API never forces
  an external consumer to depend on an internal type.
- Pre-1.0 stability note in the package doc comment (`doc.go`), as
  requested.
- `example_test.go`: `Example_fjordIntegration` — builds the exact
  `Options` shape fjord is expected to use (EKS-D `ComponentDir` +
  `ExtraAPIServerArgs` webhook flag + matching `PrebootFiles` entry,
  computing the preboot absolute-path convention from
  `Provider.KubeConfigPath` rather than hardcoding a homedir assumption).
  Does **not** call `Create` for real (a godoc `Example` with an `//
  Output:` comment executes during `go test`; actually creating a cluster
  as a side effect of running the unit test suite would be wrong) —
  documented inline why.

### cmd/rask rewired

`create.go`, `delete.go`, `get.go`, `export.go`, `load.go` now delegate to
`pkg/cluster.Provider` instead of duplicating cluster-lifecycle logic
directly against `substrate.Runtime` + `internal/cluster` + `internal/
prebake`. `addon.go` and `seed_linux.go` (`rask seed build`) were **not**
rewired — out of the literal 5-method scope the coordinator asked for
("Create/Delete/List/KubeConfigPath/LoadImages 程度"), and `rask seed
build` in particular needs `hostproc.Runtime`-specific access
(`SeedSourcePath`) `pkg/cluster.Provider` deliberately doesn't expose.

`load.go` keeps its own `internal/cluster.Exists` pre-check ahead of
spawning `docker save` (unchanged from before) — `Provider.LoadImages`
also checks existence internally (so it's safe to call standalone from a
library consumer who skipped the pre-check), but re-ordering that away
from before-the-spawn would waste a `docker save` invocation against a
cluster that doesn't exist, a real behavior regression.

## Verification

- Unit tests (all green, `-race -shuffle=on -count=1`, darwin):
  `internal/components/source_test.go` (8 new tests: cache-hit resolution
  for both sources, local-dir overlay correctness, kube-proxy exclusion,
  missing/non-executable/all-missing error clarity), `internal/bootstrap/
  config_test.go` (5 new tests for `buildAPIServerArgs`), `internal/
  manifests/{coredns,bundle}_test.go` (image-override + digest-sensitivity
  tests), `internal/prebake/key_test.go` (image-sensitivity test),
  `internal/substrate/substrate_test.go` (`StagePrebootFiles`: happy path,
  no-op-when-empty, path-traversal rejection, missing-src error),
  `internal/substrate/vz/preboot_test.go` (`buildPrebootCpio`/
  `concatInitramfs`, including a real cpio round-trip via `/usr/bin/cpio`
  extraction, matching this package's existing test convention),
  `cmd/rask/create_test.go` (6 new tests for the 4 new flags),
  `pkg/cluster/provider_test.go` (16 tests covering `Create`/`Delete`/
  `List`/`KubeConfigPath`/`KubeConfig`/`LoadImages`), plus
  `example_test.go`'s `Example_fjordIntegration`.
- `go build`/`go vet`/`golangci-lint run ./...`: clean on darwin,
  `GOOS=linux GOARCH=arm64`, and `GOOS=linux GOARCH=amd64`. (One
  pre-existing `unused` lint finding in `cmd/rask-init/rosetta.go` under
  `GOOS=linux` — confirmed via `git stash -u` to predate this session
  entirely; the disabled-Rosetta code it flags, per this repo's own prior
  documented decision. Not touched, out of scope.)
- Linux-only test packages (`internal/substrate/hostproc`, `cmd/rask`) that
  never compile/run on darwin due to `//go:build linux`: cross-compiled
  (`GOOS=linux GOARCH=arm64 go test -c`) and executed for real inside the
  running colima VM via `colima ssh`, matching this repo's established
  pattern (`test/benchmark/PROGRESS.md`). All green.
- **E2E (colima, real binaries, `test/e2e/fjord-seams.sh`, new)**: 3
  scenarios, run sequentially against the shared colima VM (which also
  hosts the user's own long-running kind clusters — never touched; cleanup
  is exclusively `rask delete cluster` by exact name, matching this repo's
  established "no pattern pkill" rule):
  - (a) `--apiserver-arg authentication-token-webhook-config-file=...` +
    `--preboot-file` together, with a dummy webhook kubeconfig pointing at
    a dead endpoint (`https://127.0.0.1:9/authenticate`). Confirms
    kube-apiserver parses and starts fine, and reaches node Ready, despite
    the webhook target being unreachable — webhook authn is looked up
    lazily per request, exactly fjord's intended usage. Also verifies the
    preboot file actually landed at the documented absolute path.
  - (b) `--component-dir` pointing at the 5 binaries copied straight out of
    rask's own `~/.rask/cache/k8s-v1.33.13-<arch>/` (no real EKS-D
    download involved) — node Ready.
  - (c) `--coredns-image` set to the exact same default image ref
    (`registry.k8s.io/coredns/coredns:v1.14.6`, extracted from source at
    script-run time rather than hardcoded a second time, so it can't
    silently drift) — CoreDNS Ready, image verified via `kubectl get deploy
    ... -o jsonpath`.
  - Result: **PASS**, all 3 scenarios, first attempt after fixing one
    script bug (the pre-run best-effort cleanup call was accidentally
    calling the *full* cleanup function, which deletes the just-built
    binary before it was ever used — fixed by splitting
    `delete_leftover_clusters` out from `cleanup`).
- **E2E re-run after the pkg/cluster CLI rewire** (`test/e2e/linux.sh`,
  unmodified, exercises the original create/CoreDNS/smoke-pod/delete
  cycle, now running entirely through `pkg/cluster.Provider` under the
  CLI): **PASS** — node Ready, CoreDNS Ready, smoke pod Running, delete
  removed the state directory and left zero rask-launched processes.
  `test/e2e/fjord-seams.sh` also re-run in full post-rewire: **PASS**,
  all 3 scenarios again.
- **External-module import check**: a throwaway module
  (`example.com/external-consumer`) with a `replace` directive pointing at
  this checkout built successfully against `pkg/cluster.NewProvider`/
  `Options`/`List`; a second file directly importing `internal/substrate`
  failed exactly as expected ("use of internal package ... not allowed"),
  confirming `NewProviderWithRuntime`'s parameter type is genuinely
  unreachable from outside this module despite being exported.
- vz (macOS) E2E: **not run**. Per this session's explicit environment
  boundary ("macOS vz E2E allowed ONLY if needed for the preboot-cpio
  path") and this repo's own prior incident history (a real host crash
  during vz E2E testing earlier this project, `research-m0-spikes.md`'s
  "Rosetta 対応の中止" note), the vz preboot-cpio path was verified via
  unit tests exercising `buildPrebootCpio`/`concatInitramfs` directly
  (including a real `cpio` extraction round-trip) instead of a full VM
  boot. `Create`'s hard-error paths for `ComponentDir`/`ExtraAPIServerArgs`/
  `CoreDNSImage` are one-line, low-risk, and covered by the fact that vz's
  package `go build`/`go vet`/`golangci-lint` all pass.

## fjord integration contract

How fjord should invoke rask, flag-by-flag (all via `rask create cluster`,
or the identical `pkg/cluster.Provider.Create` `Options` fields):

1. **`--component-dir <path>`** (`Options.ComponentDir`): point at the
   directory fjord already resolved+extracted an EKS-D
   `kubernetes-server` tarball into. Must contain exactly `kube-apiserver`,
   `kube-controller-manager`, `kube-scheduler`, `kubelet`, `kubectl`,
   executable. `kube-proxy` is never read from here — rask always sources
   it from its own default download cache, regardless of this flag.
2. **`--apiserver-arg key=value`** (repeatable; `Options.
   ExtraAPIServerArgs`): no leading `--`. Use this for
   `authentication-token-webhook-config-file` and any other apiserver flag
   fjord needs. Do **not** try to set a rask-managed flag this way
   (`etcd-servers`, `service-account-*`, `api-audiences`, `authorization-
   mode`, `service-cluster-ip-range`, `anonymous-auth`, `profiling`,
   `allow-privileged`, `client-ca-file`, `tls-*`, `kubelet-*`,
   `secure-port`, `advertise-address`) — `rask create` rejects the whole
   invocation up front with a clear error naming the offending key, rather
   than silently losing it. For extra TokenReview audiences specifically,
   use `--api-audience` instead (kept as its own flag, not folded into this
   one).
3. **`--preboot-file src=dest`** (repeatable; `Options.PrebootFiles`): use
   this to place fjord's authentication webhook kubeconfig and any TLS
   certs kube-apiserver needs to read at startup. `dest` is relative;
   its absolute in-cluster path (to build the matching `--apiserver-arg`
   value in the *same* invocation) is `<homeDir>/clusters/<name>/data/
   preboot/<dest>` — `<homeDir>` defaults to `~/.rask` (override with a
   future `--home`/`HomeDir`-equivalent if fjord ever needs a non-default
   one; none exists on the CLI today, only via `pkg/cluster.NewProvider`'s
   parameter). Files are staged before any cluster process starts, so
   they're guaranteed present by the time kube-apiserver reads them.
4. **`--coredns-image <ref>`** (`Options.CoreDNSImage`): point at fjord's
   ECR CoreDNS image mirror. Safe to pass on every create call, including
   with the exact same value every time — a matching prebaked seed (if one
   exists for that exact image) is still picked up automatically; a seed
   built for a different image is never silently reused.

All 4 are independent and composable in a single `rask create cluster`
invocation — this is exactly what `test/e2e/fjord-seams.sh` scenario (a)
exercises (`--apiserver-arg` + `--preboot-file` together) and what
`pkg/cluster/example_test.go`'s `Example_fjordIntegration` documents as a
single `Options` value combining all 4.

fjord importing `pkg/cluster` directly (in-process, no CLI shell-out) is
also fully supported and exercises identical code paths — see "pkg/cluster"
above.
