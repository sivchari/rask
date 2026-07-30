# `rask load docker-image` — progress tracker

Not committed; scratch state for this session. Closes the M2 follow-up item
"rask load docker-image + ECR pull" (RESULTS-m2.md's "What remains for M3")
for the docker-image half; ECR pull is out of scope for this session.

## Design implemented

`internal/substrate.Runtime` gained an `ImageSource{Reference, Stream}` type
and a `LoadImages(ctx, name, images []ImageSource) error` method, alongside
Exec/WriteFile/PortForward.

- **hostproc** (`internal/substrate/hostproc/loadimages.go`, new file):
  dials the cluster's own containerd instance directly at
  `dataDir/containerd/containerd.sock` (the same socket
  `internal/bootstrap/config.go`'s `writeContainerdConfig` already sets up —
  hostproc has no VM boundary, so this is host-reachable with no
  forwarding) via `github.com/containerd/containerd/v2/client`, imports each
  `ImageSource` into the `k8s.io` namespace (`WithDefaultNamespace`) with
  `client.Import(ctx, stream, containerd.WithImportPlatform(platforms.DefaultStrict()))`.
  Errors with a clear "cluster ... is not running" before ever dialing if
  the running marker isn't present.
- **vz** (`internal/substrate/vz/loadimages.go`, new file — did not touch
  any of vz's existing in-flight files, since another session was actively
  editing vz.go/agentclient.go/terminate.go this same session): stub that
  returns a clear "not implemented yet" error, matching PortForward's
  existing precedent. A vz cluster's containerd has no host-reachable
  socket (same gap as `Config.SeedPath` — see vz.go's Start doc comment);
  closing it needs a guest-side path through rask-init's control agent,
  future work, not done here.
- **cmd/rask/load.go** (new): `rask load docker-image IMAGE [IMAGE...]
  --name <cluster>` streams `docker save <ref>`'s stdout directly into
  `LoadImages`, one image (one "docker save" process) at a time — never
  more than one export in flight, both to avoid kind's resend-the-world
  bug (kind#3063, see research-m0-spikes.md) and to stay gentle on a
  single shared Docker daemon (this session's colima is 2vCPU/16GB
  shared). `rask load image-archive PATH [PATH...] --name <cluster>` loads
  a docker-save/OCI tar already on disk — no Docker daemon needed at all
  (nerdctl/podman users). Both reject up front via `cluster.Exists` if the
  named cluster was never created, before doing any work.

## Real bug found and fixed: containerd Import vs. multi-arch `docker save` archives

First live run against a retagged `busybox:latest` failed:

```
hostproc: importing rask-load-e2e:...: content digest sha256:e86d3659...: not found
```

Root cause: this session's colima dockerd (containerd-backed image store)
exports `docker save` as a full OCI layout whose `index.json` points at a
multi-platform `image.index` manifest listing every platform Docker knows
about for the image (amd64, arm/v5, arm/v6, arm/v7, arm64, ...) — but the
archive's `blobs/` directory only actually contains blobs for whichever
platform is locally present. containerd's `Import` defaults to walking
every platform in the index, and fails the instant it reaches a
declared-but-not-included foreign-platform manifest. Every archive
`LoadImages` ever receives (both `docker save`'s live stream and an
on-disk tar) was produced on this same host, so the only platform that can
ever actually be present is the host's own. Fixed by passing
`containerd.WithImportPlatform(platforms.DefaultStrict())` (see
loadimages.go's `importPlatform` doc comment). This is the same failure
mode people hit with plain `ctr images import` against a multi-arch
`docker save` tar; not rask-specific, but not something to discover live
in front of a user either.

## go.mod: pre-existing module-graph conflict, fixed with `replace`

Adding `github.com/containerd/containerd/v2/client` collided with
`gvisor.dev/gvisor` (pulled in indirectly by
`github.com/containers/gvisor-tap-vsock`, used by `internal/substrate/vz`
for macOS networking): gvisor.dev/gvisor pins a 2023-era
`github.com/containerd/containerd` and `google.golang.org/genproto`, both
from before those projects split their generated-proto packages into their
own modules (`github.com/containerd/containerd/api`,
`google.golang.org/genproto/googleapis/rpc`) while keeping the exact same
import paths under the old module — a genuine ambiguous-import conflict
(two modules providing the same import path) the instant anything in the
build needs the new split-out packages, regardless of GOOS build tags
(confirmed: reproduces even from a clean `go mod tidy` against this
session's *starting* go.mod, i.e. pre-existing latent conflict, not
something this session's other changes caused). `go mod tidy` cannot fix
this on its own — it has no notion of "bump this old module so an
unrelated package's imports stop colliding" — so two `replace` directives
were added to go.mod (with an explanatory comment inline) forcing both old
modules to versions that already depend on the split-out modules instead
of vendoring those packages themselves. Verified stable across repeated
`go mod tidy` runs; `go build`/`go vet` clean on both `GOOS=darwin` and
`GOOS=linux GOARCH=arm64`.

## Unit tests added

`internal/substrate/hostproc/loadimages_test.go` (`containerdSocketPath`
layout, "not running"/"unknown cluster" rejection before ever dialing —
the real `client.Import` path itself is not unit-tested, same established
precedent as `BuildSeed`: it needs a real containerd, verified instead by
the live E2E below), `internal/substrate/vz/loadimages_test.go` (stub
rejects when not running), `cmd/rask/load_test.go` (cluster-existence
guard rejects before ever touching Docker or opening a file; cobra
`MinimumNArgs` wiring; `image-archive`'s multi-path plumbing and
stop-on-first-failure behavior verified fully against `fakeRuntime`,
since it needs no real Docker daemon). `go test -race -shuffle=on
-count=1 ./...` green on darwin; the three linux-only hostproc test files
(20 tests total, including the 3 new ones) built as a `GOOS=linux
GOARCH=arm64 go test -c` binary and run for real inside colima — all pass.
`golangci-lint run ./...`: 0 issues.

## E2E: `test/e2e/load.sh` (new, mirrors `test/e2e/linux.sh`'s structure)

Real cycle in colima: `rask create` → retag `busybox:latest` under two
unique per-run tags → `rask load docker-image` (from colima's live
dockerd) → pod with `imagePullPolicy: Never` reaches Running →
`docker save` to a tar (copied into colima) → `rask load image-archive` →
a second pod with `imagePullPolicy: Never` against the archive-loaded tag
also reaches Running → `rask delete`. Three separate real runs, all
passed cleanly (script exit 0, no leftover cluster/processes).

Measured load time (busybox, ~1.9MB uncompressed, the only image already
cached in this colima's dockerd — smaller than the 10-50MB target range
noted in the task, but sufficient to validate the streaming path; timings
scale with image size since the entire path is a single streamed
docker-save → containerd-import, no intermediate buffering):

| path                    | run 1  | run 2  | run 3  |
|--------------------------|--------|--------|--------|
| `load docker-image`      | 624ms  | 516ms  | 426ms  |
| `load image-archive`     | 461ms  | 375ms  | 363ms  |

Qualitative comparison with kind's approach (research-m0-spikes.md): kind
`docker save`s every requested image into one combined tarball, then
`ctr import`s that whole tarball once per node *and*, per kind#3063,
redundantly resends the whole combined tarball once per requested image
even for a single node — so a 2-image load re-transfers the full
multi-image tarball twice. rask's path here never builds a combined
tarball at all: each image is `docker save`d and streamed straight into
containerd's content store as its own single-image archive, exactly once,
with zero disk buffering. Verified directly for the multi-image case too
(`rask load docker-image img1 img2 --name ...`, two retagged references
pointing at identical content): `ctr images ls` afterward showed both
images sharing the exact same content digest (`sha256:fd8d9aa6...`),
confirming layer/blob dedup via the content-addressed store rather than
two independent writes.

## Deliverable note: seam left for the future vz (guest-agent) path

`internal/substrate/vz/loadimages.go`'s stub doc comment records exactly
what future work needs to close the gap: a guest-side import path through
rask-init's control agent (`internal/guestagent`), the same pattern
Exec/WriteFile already use, since a vz cluster's containerd instance has
no host-reachable socket (its data disk is a virtio-blk block device the
guest exclusively owns). Not implemented — out of scope per this
session's explicit instruction not to touch vz/guestagent/guestinit
internals (another session was actively working on them).
