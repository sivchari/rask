# vz substrate parity — progress

Branch: `feat/vz-seams`. Goal: bring the macOS (vz) substrate to feature
parity with hostproc for fjord/EKS-D.

## Status summary

| # | Deliverable | Code | Unit tests | E2E |
|---|---|---|---|---|
| 1 | ExtraAPIServerArgs | done | done | done, passed (+ negative control) |
| 2 | CoreDNSImage | done | done | done, passed |
| 3 | ComponentDir | done | done | done, passed |
| 4 | Image prefetch | done | done | done, ~8.6s p50 improvement measured |

All four deliverables landed and are E2E-verified with real VM boots on this
host. Nothing was deliberately left unimplemented.

## Key finding: PrebootFiles was never actually reachable on vz

Before this session, `guestlayout.PrebootDir` (`/var/lib/rask/preboot`) sat
under `DataMountPoint` (`/var`). `cmd/rask-init`'s `formatAndMountDataDisk`
mounts the per-cluster ext4 data disk at `/var` *after* the initramfs (and
its preboot cpio overlay) is already unpacked into the tmpfs root — so
mounting over `/var` silently shadowed whatever the overlay placed at
`PrebootDir` (standard Unix mount semantics). Nothing in `cmd/rask-init` or
`internal/bootstrap` ever actually opened a file under `PrebootDir` before
this session (confirmed via grep), so this was fully latent — the first
real consumer would have been an `--apiserver-arg` value referencing a
preboot file, which vz previously rejected outright.

Fix: added `guestlayout.PrebootStagingDir` (`/opt/rask/preboot`, outside
`DataMountPoint`, survives the mount) as the overlay's actual destination.
`cmd/rask-init/mount.go`'s new `copyPrebootFiles()` copies that staging
content into `PrebootDir` right after `formatAndMountDataDisk` succeeds, so
`PrebootDir`'s external contract (`substrate.PrebootSubdir`'s doc comment —
what fjord computes) still resolves correctly by the time `bootstrap.Boot`
runs.

## Design: per-cluster guest config transport

New `internal/guestconfig` package (no OS dependency, built/tested on any
host): a small JSON `Config{ExtraAPIServerArgs, CoreDNSImage}`.
`internal/substrate/vz`'s `Runtime.Start` stages it to
`dataDir/cluster-config.json` (mirrors `PrebootFiles`' own staging, since
`RunVMHost` is a separate OS process); `RunVMHost` reads it back and embeds
it into a small cluster-config overlay cpio at
`guestlayout.ClusterConfigPath` (`/opt/rask/cluster-config.json`), which
`cmd/rask-init` reads at boot and threads into `bootstrap.Config.ExtraAPIServerArgs`
and `applyManifests`' CoreDNS image argument. All four overlay cpios
(preboot, cluster-config, component-override, images) are now built
independently and concatenated together as one `extra` byte slice appended
to the shared template initramfs in `vmhost.go`'s `RunVMHost`.

## Design: --component-dir (the hard one)

The shared template initramfs is still built once per host with rask's own
default binaries — never rebuilt per `--component-dir`. Instead,
`Runtime.Start` resolves the override via the same
`components.NewLocalDirSource` hostproc already uses, stages the five
overridden binaries host-side (`componentoverride.go`'s
`stageComponentOverride`), and `RunVMHost` layers them as a second cpio
archive at exactly `guestlayout.BinDir`'s paths — the same paths the
template already populated — concatenated *after* the template.

**Kernel semantics were verified, not assumed** (task requirement): fetched
`init/initramfs.c` directly from `torvalds/linux` HEAD. `do_name()` opens a
regular-file entry with `O_CREAT|O_TRUNC` unconditionally (an existing
path is only pre-`unlink`ed by `clean_path` if its *type* differs) and then
`vfs_truncate()`s it to the new entry's `body_len` — so a later archive's
entry at an already-populated path genuinely replaces its content. Also
confirmed `TRAILER!!!` does not stop the parser (`do_name` just resets to
look for another header), matching the existing `concatInitramfs` doc
comment's assumption.

A macOS-cpio-based unit test for this was attempted first and **failed** —
turned out macOS's bundled `cpio` (bsdcpio/libarchive) stops at the first
`TRAILER!!!` and never even looks at a second concatenated archive
(confirmed empirically with a standalone diagnostic). It is not a valid
stand-in for the Linux kernel's own unpacker, so that test was removed in
favor of citing the kernel source directly (see `componentoverride.go`'s
doc comment) plus a real guest-kernel boot for end-to-end confirmation
(pending, see E2E section below).

## Design: image prefetch (deliverable 4)

Mirrors `internal/substrate/hostproc`'s `importCachedImages`:
`Runtime.Create` prefetches `vzRequiredImages(coreDNSImage)` (pause +
CoreDNS + local-path-provisioner images) into a host-wide
`internal/imagebundle.Cache` under `<homeDir>/cache/images`, concurrently
with `buildTemplateInitramfs` (same goroutine-based `imagesDone` pattern
hostproc's own `Create` uses). No host↔`RunVMHost` staging step is needed
for this one (unlike preboot/cluster-config/component-override): both
processes independently derive the same cache path from the shared
`homeDir`. `RunVMHost` looks up whatever got cached and embeds it into an
images overlay cpio under `guestlayout.ImagesDir`
(`/opt/rask/images/*.tar`); `cmd/rask-init`'s new `images.go` imports each
archive into the guest's own containerd (`importPrefetchedImages`),
started concurrently with `bootstrap.Boot` inside `runBoot` and awaited
before `applyManifests` runs — same shape as hostproc's `Start`.

Deliberately did **not** touch `internal/substrate/hostproc/loadimages.go`
to share the containerd-import plumbing: extracting it into a new shared
package would touch an already-shipped, unrelated substrate for a ~50-line
DRY win with real regression risk, out of scope for a vz-parity task.
`cmd/rask-init/images.go` duplicates the same shape deliberately
(mirrors the precedent already set by `applyManifests`' own doc comment,
which explains why it isn't shared with hostproc's either).

## Files changed

- `internal/guestlayout/guestlayout.go` — `PrebootStagingDir`,
  `ClusterConfigPath`, `ImagesDir` constants + doc comments.
- `internal/guestconfig/` (new) — `guestconfig.go`, `guestconfig_test.go`.
- `internal/substrate/vz/clusterconfig.go` (new) + test — stage/load/build
  the cluster-config overlay.
- `internal/substrate/vz/componentoverride.go` (new) + test — stage/build
  the component-override overlay.
- `internal/substrate/vz/imageprefetch.go` (new) + test — prefetch +
  build the images overlay.
- `internal/substrate/vz/preboot.go` — writes into `PrebootStagingDir`
  instead of `PrebootDir`.
- `internal/substrate/vz/vz.go` — `Create` prefetches images + validates
  `--component-dir`; `Start` no longer rejects `ExtraAPIServerArgs`/
  `CoreDNSImage`, stages cluster-config + component-override.
- `internal/substrate/vz/vmhost.go` — `RunVMHost` builds and concatenates
  all four overlay cpios.
- `cmd/rask-init/mount.go` — new `copyPrebootFiles`.
- `cmd/rask-init/main.go` — calls `copyPrebootFiles`, loads
  `guestconfig.Config`, passes it to `runBoot`.
- `cmd/rask-init/boot.go` — `runBoot`/`applyManifests` take the guest
  config; starts the concurrent image-import goroutine.
- `cmd/rask-init/images.go` (new) — guest-side containerd image import.

## Verification so far

- `go build ./...` — darwin, clean.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/rask-init/...` —
  clean.
- `go vet ./...` (darwin) and `go vet` (linux/arm64, rask-init) — clean.
- `golangci-lint run` on every changed package — 0 new issues (3
  pre-existing `unused` warnings in `cmd/rask-init/rosetta.go`, confirmed
  present on `main` too, untouched — out of scope).
- `go test -race -shuffle=on -count=1` on every changed/darwin-buildable
  package — all green.
- `cmd/rask-init` has (and had, before this session) zero unit tests —
  matches existing convention (PID 1 in a guest kernel, verified via real
  boot only, not unit tests). New guest-side logic follows the same
  precedent; verified via the E2E runs below instead.

## E2E runs (all done, all passed)

Built via `make build-rask-init && make build && make codesign` in this
worktree; one VM at a time throughout, `rask delete cluster` after every
run, exact-PID cleanup only. Used the real `~/.rask` home dir (already
warm from prior sessions' component/kernel caches — confirmed no other
clusters were running before starting, and none were left behind after).

### 1. ExtraAPIServerArgs + PrebootFiles path-contract fix

```
rask create cluster --name e2e-apiserver-arg \
  --preboot-file <dummy webhook kubeconfig>=auth/webhook.yaml \
  --apiserver-arg authentication-token-webhook-config-file=/var/lib/rask/preboot/auth/webhook.yaml \
  --verbose
```

First attempt (before discovering the `templateInitramfsVersion` staleness
bug below) silently ran against a **stale cached template initramfs** —
the guest's `/init` was an old build of `cmd/rask-init` that predated
every change in this session, so `copyPrebootFiles` never ran even though
it compiled fine. Symptom: `/opt/rask/preboot/...` (the staging path)
existed and was readable via the guest agent, but
`/var/lib/rask/preboot/...` (the documented external path) did not — a
mount-shadowing bug silently going uncaught for a second reason on top of
the first. Root cause: `buildTemplateInitramfs` caches by
`templateInitramfsVersion` alone and reuses the cached file forever, and
that constant was never bumped despite rewriting `cmd/rask-init/main.go`,
`boot.go`, `mount.go` and adding `images.go`. Fixed by bumping it (`v12`
→ `v14` across this session's two rounds of rask-init edits — see
`internal/substrate/vz/initramfs.go`). Re-ran after the fix:

- Cluster booted successfully (`node_ready` at 3.07s, timeline clean).
- `data/cluster-config.json` staged exactly as designed:
  `{"extraAPIServerArgs":["authentication-token-webhook-config-file=/var/lib/rask/preboot/auth/webhook.yaml"]}`.
- Guest agent `/file?path=/var/lib/rask/preboot/auth/webhook.yaml`
  returned the staged content — the documented external path now
  resolves correctly post-mount.
- `kubectl get nodes` → `Ready`.

**Negative control** (causality check, not just correlation): same
`--apiserver-arg` pointing at a path with no matching `--preboot-file`
(`/var/lib/rask/preboot/does-not-exist.yaml`). Boot failed as expected:
kube-apiserver's own log — `E... run.go:72] "command failed"
err="stat /var/lib/rask/preboot/does-not-exist.yaml: no such file or
directory"` — then `apiserver did not become ready within 1m0s`,
`RASK-INIT-BOOT-FAILED`. Confirms the positive run's success is causally
due to the file actually being present at that path, not an unrelated
pass. Cluster's failure-cleanup path removed all state automatically
(`cluster.Dir` gone, no orphaned process) — a `SIGKILL still didn't exit`
warning was logged during cleanup but the process was independently
confirmed gone via `ps` immediately after; pre-existing `terminate.go`
logic, untouched by this session.

### 2. CoreDNSImage

```
rask create cluster --name e2e-coredns-image \
  --coredns-image registry.k8s.io/coredns/coredns:v1.11.3 --wait coredns --verbose
```

- `kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].image}'`
  → `registry.k8s.io/coredns/coredns:v1.11.3`.
- CoreDNS pod `1/1 Running`.
- `RASK-INIT-IMAGES-IMPORTED count=4` in the guest console log (pause,
  coredns:v1.11.3, local-path-provisioner, its busybox helper image) —
  confirms the images overlay + guest-side containerd import
  (deliverable 4's actual mechanism) fired correctly for a
  non-default CoreDNS image too, not just the pinned default.
- `~/.rask/cache/images/arm64/` on the host contains both
  `registry.k8s.io_coredns_coredns_v1.14.6.tar` (from earlier default
  runs) and `..._v1.11.3.tar` (this run) — confirms `Runtime.Create`
  correctly prefetches whichever CoreDNS image a given cluster actually
  requested, not a single hardcoded ref.

### 3. ComponentDir

Downloaded a full, different (older) real k8s release
(`v1.33.10`, vs. rask's pinned default `v1.33.13`) for `linux/arm64` — all
five overridden binaries (kube-apiserver, kube-controller-manager,
kube-scheduler, kubelet, kubectl) — into a local directory and:

```
rask create cluster --name e2e-component-dir \
  --component-dir <dir> --verbose
```

- Boot succeeded (timeline clean, `node_ready` at 2.97s) — proves the
  override binaries are not just present but fully functional as the
  cluster's actual control plane and kubelet.
- `kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}'`
  → `v1.33.10` (not the template's pinned `v1.33.13`) — direct proof the
  component overlay cpio's later entries shadowed the template's default
  binaries at the exact same guest paths, for a real kubelet actually
  registering the node.
- `kubectl version` → `Server Version: v1.33.10` — kube-apiserver override
  confirmed too.
- This is the empirical, real-kernel confirmation the design's kernel
  source analysis (see above) predicted: concatenating a second cpio
  archive with entries at the template's own paths, after the template,
  causes the kernel's initramfs unpacker to end up running the later
  entries' binaries — demonstrated with the actual guest kernel, not a
  substitute tool.

### 4. Image prefetch timing (p50, n=5 each)

Two binaries: this branch's worktree build ("after") vs. the pre-existing
`main`-branch build already present in the primary checkout ("before" —
confirmed via `git log`/`--help` flag inspection to be the unmodified
base commit, no image prefetch code at all). Both point at the same
`~/.rask` home dir (shared, already-warm component + image cache), so the
only variable under test is whether image prefetch/import runs at all.
`rask create cluster --wait coredns` timed end-to-end (wall clock around
the whole CLI invocation), `rask delete cluster` between every run, one
VM at a time.

- Before: `35.76s, 19.01s, 17.74s, 36.75s, 25.78s` → **p50 = 25.78s**
- After: `13.37s, 13.66s, 17.21s, 19.02s, 18.09s` → **p50 = 17.21s**
- **Δp50 ≈ 8.6s faster**, consistent in direction and rough magnitude
  with the task's "~+10s" estimate for a live CoreDNS pull. "Before" had
  high run-to-run variance (17–37s), most plausibly real registry
  pull-latency variance for `registry.k8s.io`/`docker.io` at test time —
  the honest number is the delta between medians, not any single run.

Cleaned up after every single run throughout (`rask delete cluster`,
verified via `ps`/`~/.rask/clusters` that no VM or cluster state was ever
left behind between runs). `internal/substrate/vz/embedded/rask-init`
restored to the placeholder (`git checkout --`) once all E2E work
finished — confirmed via `git status`.
