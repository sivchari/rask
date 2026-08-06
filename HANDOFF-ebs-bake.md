# Handoff: baking rask into haro's node image-cache EBS snapshot

This is the contract between rask's release process and any consumer
(haro, initially) that wants to bake a pre-built `rask` binary — with its
cache pre-warmed — into a machine image or volume snapshot, so a fresh
workspace starts `rask create` with zero network access and zero wait.

## What "bundled" means

Every rask release is bundled: the binary has its full component payload
(Kubernetes binaries, kine, containerd, runc, the CNI plugins) **and**
every container image the default cluster needs (CoreDNS, the CRI pause
image, local-path-provisioner) embedded at build time
(`internal/components/bundlepayload`), for one fixed (Kubernetes version,
architecture) target. There is no other kind of
release — a slim, download-on-first-use binary used to also be published,
but a cold-cache download eating the guest-boot deadline was a real
production failure (`rask create` timing out after ~5 minutes on a slow
network), so it no longer ships.

Everything else about the binary is identical: same CLI, same flags, same
`~/.rask` layout, same behavior for a caller like
[`pkg/cluster`](pkg/cluster) (fjord imports rask this way). The only
difference from an unbundled source build is where component bytes come
from the first time they're needed — memory instead of the network.

## Artifact naming and where to get it

Each tagged release (`vX.Y.Z`) is built by
[`.github/workflows/tagpr.yaml`](.github/workflows/tagpr.yaml) (goreleaser,
configured by [`.goreleaser.yml`](.goreleaser.yml) for Linux and
[`.goreleaser-darwin.yml`](.goreleaser-darwin.yml) for macOS) and attached
to that tag's GitHub Release once both platforms finish:

| archive | platform |
|---|---|
| `rask-bundled_<version>_linux_amd64.tar.gz` | amd64 workspace nodes (haro's primary target) |
| `rask-bundled_<version>_linux_arm64.tar.gz` | Linux arm64 hosts |
| `rask-bundled_<version>_darwin_arm64.tar.gz` | Apple Silicon macOS (vz substrate; also carries the guest kernel + userland) |

`<version>` has no leading `v` (e.g. `0.1.6` for tag `v0.1.6`). Each
archive contains one binary, `rask-bundled`, plus `LICENSE`.

Checksums for the two Linux archives are in `rask_<version>_checksums.txt`;
the darwin archive's checksum is in `rask_<version>_darwin_checksums.txt`
(a separate file — the darwin and Linux goreleaser runs each write their
own). Both are attached to the same release.

## Checksum verification

Verify before trusting the binary, every time — treat a mismatch as a hard
failure, not a warning:

```sh
curl -fLO https://github.com/sivchari/rask/releases/download/<tag>/rask-bundled_<version>_linux_amd64.tar.gz
curl -fLO https://github.com/sivchari/rask/releases/download/<tag>/rask_<version>_checksums.txt
sha256sum -c --ignore-missing rask_<version>_checksums.txt
```

## Bake procedure

1. Download and verify `rask-bundled_<version>_linux_amd64.tar.gz` (above).
2. Extract it and `chmod +x rask-bundled` somewhere on `$PATH` (or note its
   full path).
3. With `$HOME` set to whatever home directory the baked image's workspace
   process will use (rask always resolves state under
   `os.UserHomeDir() + "/.rask"`; there is no `--home-dir` override), run:

   ```sh
   rask-bundled pull
   ```

   This materializes `~/.rask/cache` (component binaries under `cache/`,
   container images under `cache/images/`) from the binary's embedded
   payload — no network access required, and it fails loudly instead of
   silently downloading if run against a non-bundled binary (it never is,
   for a released artifact, but this is the guard that makes the failure
   mode obvious if the wrong binary ever ends up in the bake pipeline).
4. Snapshot the volume/AMI with the binary and the now-warm
   `~/.rask/cache` both present. A workspace booted from it runs
   `rask create cluster` with no cold-cache download at all.

`~/.rask/cache` does not otherwise need any directory structure pre-seeded
beyond what `pull` creates — `rask create` populates the rest of
`~/.rask` (per-cluster state) at cluster-creation time regardless.

## Privileged requirement (recap)

On Linux, rask runs Kubernetes control-plane and node components as plain
supervised host processes (`internal/substrate/hostproc`) and assumes it
owns the host's network namespace (CNI bridge, kube-proxy iptables rules).
It works inside a container, but only a `--privileged` one — this is
unchanged by bundling or by baking; a baked binary needs exactly the same
privileges an unbaked one does. See the main
[README](README.md#requirements) for the up-to-date statement of this
requirement.

## Version-update flow — when to recut the snapshot

A bundled binary's embedded payload is pinned at build time to:

- the Kubernetes version (`components.DefaultK8sVersion`)
- kine, runc, containerd and CNI-plugins versions
  (`internal/components/components.go`'s `KineVersion`/`RuncVersion`/
  `ContainerdVersion`/`CNIPluginsVersion`)
- the cluster's container images: `components.PauseImage` (the CRI sandbox
  image), `manifests.CoreDNSImage`, and local-path-provisioner's own pinned
  images (`internal/manifests/local-path-storage.yaml`) — see
  `internal/imagebundle.RequiredImages`, the one function that composes all
  three sources into the ref list `cmd/bundle-payload` stages and a bundled
  `rask pull` extracts. There is no separate "image version" to track
  independently of these three sources; bumping any of them bumps what a
  new bundled binary ships, with nothing else to edit.

It never re-fetches or auto-updates these at runtime — an old snapshot's
binary keeps working, but keeps provisioning clusters on whatever versions
(Kubernetes and images alike) it was built against, until the snapshot is
recut with a newer artifact and a fresh `rask pull`. Recut when:

1. **A new rask tag bumps any of the pinned versions above.** Check
   `internal/components/components.go`, `internal/manifests/coredns.go` and
   `internal/manifests/local-path-storage.yaml` (or the release notes) for
   a version diff before deciding whether a recut is needed for a given
   release — not every rask release changes pinned component or image
   versions.
2. **A pinned component or image gets a security patch** (e.g. a runc,
   containerd or CoreDNS CVE fix) — recut immediately on the release that
   includes it, don't wait for the next scheduled cadence.

There is no automatic recut trigger from rask's side — this is a pull, not
a push, relationship: haro's pipeline should watch
[GitHub Releases](https://github.com/sivchari/rask/releases) (or `git log`
on `internal/components/components.go` and `internal/manifests/coredns.go`)
and decide when to act on a new artifact.

## What this handoff does not cover

- Functional verification of a specific artifact: build/publish is covered
  by CI, but `rask pull` followed by a real `rask create cluster --wait
  node` from the warm cache is worth running at least once per bake
  pipeline change, outside this repo's own CI.
