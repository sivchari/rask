# Bundled build — progress

Branch: `feat/bundled-build` (worktree: `.claude/worktrees/feat-bundled-build`)

## Environment note

`go.work` at the main repo root (`/Users/takuma.shibuya.001/workspace/sivchari/rask/go.work`)
leaks into this worktree's `go` command resolution because the worktree is
nested under the main repo (`.claude/worktrees/...`) and `go.work`
auto-detection walks up past the worktree root. Every `go`/`make` command
in this worktree needs `GOWORK=off` (confirmed the sibling
`embed-placeholder` worktree has the same issue). Not something to fix in
this task — just a local invocation quirk.

## Status: core done, linux/amd64 payload staged locally, workflow+handoff written

### 1. Core (internal/components embedded source)

- Removed the redundant `EmbeddedSource` `ComponentSource` wrapper type
  the prior agent had started in `embeddedsource.go`: it only ever called
  `cache.Ensure`, identical to `DownloadCacheSource.Resolve` — pure
  duplication once `DefaultCache`'s transport-level fallback exists.
  Replaced with `internal/components/defaultcache.go`, keeping just
  `DefaultCache(dir) *Cache` (network-backed via `NewCache` when
  `bundlepayload.Available()` is false, embedded-payload-backed via
  `NewCacheWithTransport` otherwise). Every existing `ComponentSource`
  (`DownloadCacheSource`, `LocalDirSource`) gets embedded resolution for
  free through the `Cache` it wraps — no new ComponentSource needed.
- Wired the 3 real `components.NewCache(...)` call sites to
  `components.DefaultCache(...)`: `internal/substrate/vz/vz.go:112`
  (`Create`'s template-initramfs cache), `internal/substrate/vz/vmhost.go:66`
  (`RunVMHost`'s cache — guest kernel + full vz guest userland),
  `internal/substrate/hostproc/hostproc.go:166` (`componentSource` — the
  linux hostproc k8s-binary path). These three are the only places that
  matter: `internal/guestinit/wantedmodules_test.go`'s `NewCache` call is
  test-only and deliberately left alone.
- Exported `bundlepayload.ManifestPath` (was unexported `manifestPath`) so
  `cmd/bundle-payload` and `bundlepayload.loadManifest` agree on the
  on-disk manifest location without duplicating the string, matching how
  `BlobPath` already works.
- Fixed a `staticcheck` SA9009 false positive in `bundlepayload/payload.go`
  (a doc comment line started with `// go:embed`, which staticcheck reads
  as an accidentally-spaced compiler directive) by rewording.
- Wrote `internal/components/defaultcache_test.go`: pins that a slim build
  (`bundlepayload.Available() == false`, always true outside `-tags
  bundle` with a staged payload) makes `DefaultCache` behave identically
  to `NewCache` — same `Dir()`, same `http.DefaultClient`, no transport
  override.
- `golangci-lint run ./...` and `go vet ./...` (GOWORK=off) are clean, both
  with and without `-tags bundle`.

### 2. cmd/bundle-payload (payload staging tool)

New: `cmd/bundle-payload/main.go` + `main_test.go`. Downloads every URL
`components.PinnedURLs(k8sVersion, arch, guest)` returns for one
`-target` (`linux/amd64` | `linux/arm64` | `darwin/arm64` — the platform
matrix baked directly into a `map[string]target` rather than exposed as
independent `-arch`/`-guest`/`-os` flags, so an invalid combination like
`linux/amd64` + guest can't be expressed at all) and writes raw response
bytes + `manifest.json` into `internal/components/bundlepayload/payload`,
in the exact layout `bundlepayload.BlobPath`/`ManifestPath` define.
Resumable (skips a blob already on disk), writes via temp-file-then-rename
so a killed run never leaves a truncated blob. No separate checksum
verification in the tool itself — deliberate: the checksum sidecar URLs
are captured as blobs too, so `Cache.verify`'s existing sha256 check
(and `EnsureGuestKernel`'s hardcoded-constant check) already re-verifies
every fetch at `rask create` time exactly as it always has, whether the
bytes came from the network or the embedded payload.

Tests (`main_test.go`, httptest-based, no real network) cover: fetch +
write, skip-if-already-staged, non-OK-status leaves no file (and no
leftover `.tmp`), manifest JSON round-trips through
`bundlepayload.Manifest`, and `guest` is only ever true for
`darwin/arm64` in the `targets` map.

### 3. Makefile

Added `TARGET`/`K8S_VERSION` variables, `bundle-payload` (`go run
./cmd/bundle-payload -target $(TARGET) ...`) and `bundle` (`build-rask-init`
→ `go build -tags bundle -o rask ./cmd/rask` → codesign on darwin, same
entitlement the plain `codesign` target uses) targets. `.gitignore` gained
an entry for `internal/components/bundlepayload/payload/*` (everything
except `.gitkeep`) — the real payload is gigabytes, never committed, same
pattern as `/rask-init`.

Verified `make bundle` (no payload staged) produces the same
byte-for-byte-unchanged slim-equivalent behavior: built and ran (darwin
arm64 host) a `-tags bundle` binary against an empty payload dir, 69MB,
correctly codesigned. Restored the `internal/substrate/vz/embedded/rask-init`
placeholder afterward (`git checkout --`) — never commit the real
cross-compiled binary, same rule as the existing `build`/`codesign`
targets.

### 4. .github/workflows/release-bundle.yaml (new, ci.yaml untouched)

Matrix: `linux/amd64` (`ubuntu-latest`), `linux/arm64`
(`ubuntu-24.04-arm` — same runner label `kernel.yaml` already uses
successfully in this repo), `darwin/arm64` (`macos-latest`). Each builds
natively (no cross-compilation — darwin/arm64 needs a real macOS
toolchain for codesign + Virtualization.framework cgo linkage, and the
linux jobs match that rather than cross-compiling from macOS).
Triggers: `push: tags: v*` and `workflow_dispatch`. Every run uploads a
plain workflow artifact (`rask-bundled-<os>-<arch>` + `.sha256`); a tag
push additionally attaches both files to that tag's GitHub Release
(created via `gh release create --generate-notes` if missing, `gh release
upload --clobber`). Artifact size is reported into `$GITHUB_STEP_SUMMARY`.
No functional smoke test in the workflow itself — build + checksum +
upload only, matching the plan's "linux/amd64: build + size report only,
execution verification is CI's/real node's job" (though the *repeated*
execution verification for every future artifact is really the
consuming pipeline's job going forward; this session did it once, locally,
for linux/arm64 — see below).

Validated the YAML parses (`ruby -ryaml`, `actionlint` not installed
locally so this is syntax-only, not an actions-schema check).

### 5. HANDOFF-ebs-bake.md (new, committed)

Contract doc for haro: artifact naming/location, checksum verification
steps, placement (binary anywhere executable on `$PATH`, `~/.rask/cache`
self-populates offline on first use, no flag needed), the privileged-container
requirement recap (sourced from `README.md#requirements`, not invented),
and a version-update/recut-trigger flow tied to
`internal/components/components.go`'s pinned version constants. Explicitly
scopes out container-image bundling (not implemented, `InstallImages` is
a forward-compat no-op today) and states plainly that this session did not
functionally verify the linux/amd64 artifact itself (only linux/arm64, in
colima — see below).

## Payload staging + builds, all 3 targets

Each `make bundle-payload TARGET=...` run was moved aside afterward
(`internal/components/bundlepayload/payload-<target>/`, purely local —
gitignored, not part of any commit) so the next target could stage into a
clean `payload/` for the next build, keeping every `rask` binary build a
true single-target artifact. All three staging directories were removed
once every build + size measurement below was taken — they were pure
local scratch, not part of this commit; re-running `make bundle-payload
TARGET=...` reproduces them.  `k8sVersion=v1.33.13` throughout
(`components.DefaultK8sVersion`).

| target | payload URLs | payload size on disk | slim binary | bundled binary |
|---|---|---|---|---|
| linux/amd64 | 20 (no guest) | 593M | 69M | 662M |
| linux/arm64 | 20 (no guest) | 555M | 65M | 619M |
| darwin/arm64 | 40 (+ vz guest kernel/userland) | 625M | 66M | 658M |

linux binaries above are `CGO_ENABLED=0` (static, per the Makefile's
`bundle` target); slim sizes are the equivalent plain `go build` for
comparison, not committed anywhere.

## linux/arm64 offline-cold verification (colima)

Ran in a privileged Debian container inside the existing shared colima VM
(did not start a new VM; other real containers — fjord/haro/kind clusters
etc. — kept running throughout, untouched). Two things needed adjusting
from the original plan before this worked correctly, both **pre-existing
constraints of this specific colima setup, unrelated to the bundling
change**, worth recording in case they recur:

1. **Don't bind-mount `$HOME` from the macOS host (virtiofs).** kine and
   containerd's own gRPC unix-socket listeners `chmod` their socket file
   on creation, which fails with `invalid argument` on a virtiofs-backed
   path. Using the container's own native (overlay2) filesystem for
   `$HOME` instead — which starts genuinely empty, satisfying "empty
   `~/.rask`" — fixed this immediately. Only the `rask` binary itself was
   bind-mounted in (a plain file read, unaffected).
2. **`--network host --cgroupns=host` is needed for kube-proxy to become
   ready**, not the default bridge network. Under the default bridge
   network, kubelet hit `cannot enter cgroupv2 "kubepods" with domain
   controllers -- it is in an invalid state` (nested private cgroupns) and,
   after fixing that with `--cgroupns=host`, kube-proxy then hit
   `open /proc/sys/net/netfilter/nf_conntrack_max: permission denied`
   (a non-host network namespace can't own that sysctl even
   `--privileged`). `--network host` sidesteps both — and matches
   README's own stated deployment model ("rask assumes it owns the host's
   network"), so this is arguably the *more* representative test setup,
   not a workaround.

Since `--network host` makes docker ignore `--dns`, network-blocking used
`--add-host dl.k8s.io:127.0.0.1 --add-host github.com:127.0.0.1` instead
(works regardless of network mode) — any attempted component fetch would
have hit an immediate connection-refused, not a slow timeout, so a false
"success" from a hung-then-serendipitously-fine request is ruled out. A
base image with `iptables` preinstalled (`apt-get install -y iptables`,
network-enabled, then `docker commit`) was prepared first — a real base
image build step, analogous to what an AMI/node image already provides,
not something the offline test itself should need network for.

**Result** (`rask create cluster --wait node`, both runs against the same
DNS-blocked, empty-then-warm `~/.rask`):

- **Cold** (empty `~/.rask/cache`): `real 0m4.775s`, exit 0.
  `~/.rask/cache` fully populated afterward
  (`k8s-v1.33.13-arm64/`, `containerd-2.3.3-arm64/`, `runc-v1.5.1-arm64/`,
  `kine-v0.16.3-arm64/`, `cni-plugins-v1.9.1-arm64/`) — entirely from the
  embedded payload; `dl.k8s.io`/`github.com` were unreachable throughout.
- **Warm** (same cache, second cluster): `real 0m3.736s`, exit 0.

**Cold ≈ warm (4.8s vs 3.7s)** — this is the headline result: a bundled
binary's first `rask create` on a brand-new node pays no network penalty
at all, matching README's ~4.0s p50 claim. The only network attempt
either run made was the CoreDNS/pause **image** prefetch
(`registry.k8s.io`, non-fatal, "falling back to a live pull on demand") —
expected and explicitly out of scope (see HANDOFF's scope section);
component *binaries* never touched the network.

All verification containers/images/dirs were removed after
(`docker rmi rask-verify-base`, no containers left running — confirmed via
`docker ps -a`; local `.verify/` scratch dir under the worktree removed).
No colima VM was created or destroyed; the existing shared one was reused
throughout and left exactly as found.

## darwin/arm64: build + optional live verification

`make bundle-payload TARGET=darwin/arm64` staged 40 URLs (the 20 shared
with the linux targets plus the vz guest kernel/userland — guest kernel,
busybox, iptables, gcompat, e2fsprogs, CA bundle). `make bundle` produced
a 658M codesigned Mach-O arm64 binary (vs 66M slim), codesign confirmed by
`codesign`'s own "replacing existing signature" message.

Went beyond the plan's "build + size only" and did the optional live
check too, since the codesigned binary was already in hand and no
additional VM infrastructure was needed (vz runs natively on this host, no
colima/docker involved): with `$HOME` pointed at a fresh empty scratch
directory (never touching this machine's real `~/.rask`, which has
genuine pre-existing cluster state), confirmed no vz VM was already
running (`ps aux | grep vm-host` — only one may run at a time), then:

- `rask create cluster --name verify-darwin --wait node`: `13.375s total`,
  exit 0, against a completely empty `~/.rask` — guest kernel + full
  userland resolved from the embedded payload (the darwin path's
  `Create`/`RunVMHost` cache is the one built from `DefaultCache`, wired
  in this session — see the core section above).
- `rask delete cluster --name verify-darwin`: exit 0, clean.
- Confirmed via `ps aux` that no vm-host process was left running
  afterward, then removed the scratch `$HOME` directory entirely.

This run was **not** network-isolated (unlike the linux/arm64 colima test)
— it's a functional smoke check, not a repeat of the offline-network
proof, matching the plan's lower bar for this target ("live run
optional"). The linux/arm64 result above is what backs the "zero network
component downloads" claim.

## Cleanup performed

- All three `payload-<target>/` local staging directories removed after
  their size was recorded (scratch, gitignored, reproducible via
  `make bundle-payload TARGET=...`).
- `internal/components/bundlepayload/payload/` restored to just
  `.gitkeep` (matches its committed state).
- `internal/substrate/vz/embedded/rask-init` restored to the committed
  placeholder (`git checkout --`) after every `make bundle`/`make build`
  run in this session — never left as the real cross-compiled binary.
- Every `rask`/`rask-bundled-*`/`rask-slim-*` build artifact under the
  worktree root or `/tmp` removed.
- docker: `rask-verify-base` image removed, no leftover containers
  (`docker ps -a` clean both times checked).
- Confirmed final state: `go build ./...`, `go vet ./...`,
  `go test -race -shuffle=on -count=1 ./...` (18 packages, darwin host —
  `internal/substrate/hostproc` is Linux-only via its own `//go:build
  linux`, excluded here exactly as it always has been) and
  `golangci-lint run ./...` all clean on the final worktree state.

## Done

- [x] Committed to `feat/bundled-build` (commit `6aea621`, 21 files
      changed). Not pushed — the orchestrator merges, per this session's
      environment rules.
- [x] Final report sent to the coordinator.
