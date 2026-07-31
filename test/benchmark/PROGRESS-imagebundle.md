# Image-bundle: eliminating registry pulls from `rask create --wait=coredns`

## Goal

`--wait=coredns` was ~4x slower than `--wait=node` on both substrates, and both
`RESULTS-linux.md` and `RESULTS-darwin.md` already root-caused it: `rask delete` wipes the
cluster's containerd root, so every `--wait=coredns` run pays a cold registry pull of
CoreDNS/local-path-provisioner (and, implicitly, `registry.k8s.io/pause`) that a warm-cache
kind/k3d node never pays. This session prefetches those images once (pure-Go, no daemon) and
imports them into the cluster's containerd from local disk instead, concurrently with control-plane
boot.

## Design implemented (Linux / hostproc)

1. **`internal/imagebundle`** (new package) — `Cache.Ensure`/`EnsureAll` pull an image via
   `google/go-containerregistry`'s `crane` (already an indirect dependency; promoted to direct) and
   save it as a docker-save-style tar under `~/.rask/cache/images/<arch>/<sanitized-ref>.tar` —
   the exact archive format `substrate.Runtime.LoadImages` already knows how to import (see
   `internal/substrate/hostproc/loadimages.go`'s doc comment), so no new import code path was
   needed. `Cache.Lookup` is a pure local-disk check (no network), used by the create-time import
   path so it never touches the network on the critical path.
2. **`internal/manifests.RequiredImages(coreDNSImage)`** (new) — derives the local-path-provisioner
   and helper-pod (`busybox`) image refs straight from the embedded `local-path-storage.yaml` via a
   plain line scan (`image:` mapping keys), rather than hardcoding a second copy that could drift.
   Plus `coreDNSImage` itself (respects `--coredns-image`). `hostproc.requiredImages` adds
   `components.PauseImage` (promoted from a private `internal/bootstrap` const to an exported one,
   since two packages now need it) on top — the CRI sandbox image containerd's own config pins for
   every pod, not part of any applied manifest.
3. **`hostproc.Runtime.Create`** — prefetches every `requiredImages(coreDNSImage)` ref, for the
   host's own arch, concurrently with the existing component-binary download. Best-effort: a
   failure here is logged to stderr and never fails `Create` — this matters for
   `--component-dir`, whose whole point is avoiding a hard network dependency; requiring image
   registry access instead would have silently regressed that.
4. **`hostproc.Runtime.Start`** — spawns `importCachedImages` as its own goroutine (see next
   paragraph for why not an `errgroup`), which opens whatever `internal/imagebundle` already
   cached, waits (via `net.Dial("unix", ...)` polling, bounded to 30s) for the cluster's own
   containerd socket to come up, and imports every cached archive — concurrently with
   `bootstrap.Boot`, not after it. Start still waits for this goroutine before returning (clean
   semantics, no dangling background work outliving the call), but since the import is local-disk-only
   and finishes in well under a second, it adds no serial time on top of `Boot`'s own ~2s.
   `loadimages.go`'s `LoadImages` and this path now share one `importImages` helper.

**Important concurrency note found while implementing this**: the natural-looking refactor —
wrap the `bootstrap.Boot` call and the image-import goroutine in a shared
`errgroup.WithContext(ctx)`, so both share one context — is a real bug, not just a style choice.
`internal/bootstrap/boot.go`'s `runBootDAG` doc comment already documents why: an errgroup-derived
context is canceled the moment `Wait()` returns, **including a successful return**, and
`Supervisor`-launched processes (kube-apiserver, kubelet, ...) are tied to whatever context
`Boot` receives via `exec.CommandContext` — they're meant to outlive `Start` entirely. Passing an
errgroup-derived context into `Boot` would SIGKILL the entire control plane the instant boot
succeeds (this exact bug was already hit and documented once, elsewhere, per that doc comment).
`Start`'s image-import goroutine is therefore synchronized via a plain channel
(`imagesDone := make(chan struct{})`), and `Boot` keeps receiving `Start`'s own unmodified `ctx`,
exactly as before this change.

## Deviation from the brief: macOS/vz not implemented this session

The brief's step 3 asked for the vz substrate too ("same cache, imported through the guest
agent's channel or ... the data-disk/initramfs seam ... pick pragmatically"). Investigated both,
implemented neither, for a concrete structural reason found during investigation (not a time
guess):

- **Guest-agent HTTP channel** (`internal/guestagent`, `cmd/rask-init/agent.go`): mechanically
  easy to add (`PathImportImage`, mirroring the existing `PathFile`/`PathExec` handlers, guest-side
  containerd import identical to hostproc's) — but **it cannot help `--wait=coredns` as currently
  structured**. `cmd/rask-init/boot.go`'s `runBoot` calls `applyManifests` (which creates the
  CoreDNS Deployment) synchronously, *before* `serveAgent` starts listening. By the time the host
  could ever reach a hypothetical `ImportImage` endpoint, the Deployment object already exists and
  kube-scheduler/kubelet are already free to start pulling. Adding this endpoint would only fix
  `rask load`'s existing "not implemented yet for the vz substrate" gap (a real, separate
  improvement, just not this one), not the benchmark this session is about.
- **Data-disk/initramfs seam** (`buildPrebootCpio`/`concatInitramfs`, `internal/substrate/vz/preboot.go`):
  the only path that could genuinely run image import *before* `applyManifests`, by staging the
  cached tar archives into the guest before boot (same mechanism `--preboot-file` already uses).
  Structurally sound, but `concatInitramfs` currently reads the entire template + extra payload
  into memory and writes one combined initramfs file per `Start` call — adding the ~40-100MB these
  four images take (CoreDNS + local-path-provisioner + busybox + pause) to every single `rask
  create` is a real, unmeasured cost (kernel unpack time, disk I/O, guest RAM pressure) that could
  plausibly offset or regress the very thing being optimized. This needs its own dedicated
  before/after measurement on real vz hardware before it can be trusted, which this session's
  remaining budget did not allow for safely (a regression here would be worse than doing nothing,
  and vz VMs on this shared macOS host need one-at-a-time, cleanup-after-each-run discipline that
  doesn't compose well with "try it and see").

**Concrete follow-up**, in priority order: (1) implement the initramfs-seam approach behind the
same `internal/imagebundle.Cache` (already arch-aware; vz only ever needs `arm64`, Rosetta is
disabled), gated on a real before/after `--wait=coredns` vz measurement showing it's a net win
before merging; (2) separately, implement the guest-agent `ImportImage` endpoint anyway, since it
closes `vz.LoadImages`'s existing "not implemented" gap for `rask load` regardless of the
boot-time ordering issue above.

## Verification

- Unit tests: `internal/imagebundle` (cache-key derivation, `Lookup`/`Ensure`/`EnsureAll`,
  concurrent-fetch ordering and failure propagation — against a real in-process, network-free OCI
  registry via `go-containerregistry/pkg/registry`, not a real network), `internal/manifests`
  (`RequiredImages`/`imagesFromYAML` table tests), `internal/substrate/hostproc` (socket-wait
  timeout/success, `importCachedImages`'s empty-cache fast path, `imageCacheDir` scoping).
- `go build`/`go vet`/`golangci-lint run` clean for both `GOOS=darwin` and `GOOS=linux`.
- `go test -race -shuffle=on -count=1 ./...` clean on darwin (every darwin-buildable package,
  including the new ones). `hostproc`/`imagebundle` additionally cross-compiled and run directly
  inside a real Linux (arm64, colima) VM — `-race` could not be cross-compiled from this darwin
  host for `GOOS=linux GOARCH=arm64` (no working `CGO_ENABLED=1` cross toolchain for that target
  was available; `zig cc` gets close but its aarch64 target is missing LSE-atomics symbols
  `-race`'s runtime needs — a known Zig/compiler-rt gap, not a code issue), so the Linux run was
  race-detector-free. Reviewed the new goroutine's synchronization by hand instead (see the
  concurrency note above); no shared mutable state crosses goroutines except via the `imagesDone`
  channel and `errgroup`-owned `paths[i]` slice indices in `imagebundle.EnsureAll` (each index
  written by exactly one goroutine).
- Real Linux E2E before/after benchmark: see `RESULTS-linux.md`'s new "Image-bundle" section.
  `--wait=coredns` p50 15.278s → 10.558s (-30.9%, warm image cache); `--wait=node` unaffected
  (2.364s → 2.518s, within noise). Cold-image-cache first-create measured separately: 14.853s,
  close to the old baseline as expected (see that section for why).

## Files changed

- `internal/imagebundle/imagebundle.go`, `imagebundle_test.go` (new)
- `internal/manifests/images.go`, `images_test.go` (new)
- `internal/components/components.go` — exported `PauseImage`
- `internal/bootstrap/config.go` — uses `components.PauseImage` instead of a private const
- `internal/substrate/hostproc/hostproc.go` — `Create`/`Start` wiring, `requiredImages`,
  `importCachedImages`, `imageCacheDir`
- `internal/substrate/hostproc/loadimages.go` — extracted `importImages`, added
  `waitContainerdSocket`
- `internal/substrate/hostproc/hostproc_test.go`, `loadimages_test.go` — new tests
- `test/benchmark/RESULTS-linux.md` — new "Image-bundle" section
