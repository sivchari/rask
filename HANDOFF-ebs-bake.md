# Handoff: baking a bundled rask into haro's node image-cache EBS snapshot

This is the contract between rask's release process and any consumer (haro,
initially) that wants to bake a pre-built `rask` binary into a machine
image or volume snapshot so a fresh node can run `rask create` fully
offline — no dl.k8s.io/GitHub/Alpine access required.

## What "bundled" means

A normal (`make build`) rask binary downloads every component it needs
(Kubernetes binaries, kine, containerd, runc, the CNI plugins) from the
network on first use, caching them under `~/.rask/cache`. A **bundled**
binary (`make bundle`, see the Makefile and `cmd/bundle-payload`) has that
same payload embedded into the executable at build time
(`internal/components/bundlepayload`), for one fixed
(Kubernetes version, architecture) target.

Everything else about the binary is identical: same CLI, same flags, same
`~/.rask` layout, same behavior for a caller like
[`pkg/cluster`](pkg/cluster). The only difference is where component bytes
come from the first time they're needed — memory instead of the network —
and that only matters for the very first `rask create` on a given node;
every one after it hits the warm on-disk cache exactly like today.

## Artifact naming and where to get it

Each tagged release (`vX.Y.Z`) triggers
[`.github/workflows/release-bundle.yaml`](.github/workflows/release-bundle.yaml),
which builds one binary per platform and uploads it, plus a sha256
checksum file, to that tag's GitHub Release:

| file | platform |
|---|---|
| `rask-bundled-linux-amd64` | Bottlerocket amd64 workspace nodes (haro's primary target) |
| `rask-bundled-linux-arm64` | Linux arm64 hosts |
| `rask-bundled-darwin-arm64` | Apple Silicon macOS (vz substrate; also carries the guest kernel + userland) |

Each has a matching `<name>.sha256` file alongside it.

`workflow_dispatch` runs (manual, or against a non-tag ref) build the same
artifacts but only upload them as plain GitHub Actions workflow artifacts
(no release, since there's no tag to attach to) — use those for a
pre-release smoke test of the pipeline itself, not for a production snapshot
bake.

## Checksum verification

Verify before trusting the binary, every time — treat a mismatch as a hard
failure, not a warning:

```sh
curl -fLO https://github.com/sivchari/rask/releases/download/<tag>/rask-bundled-linux-amd64
curl -fLO https://github.com/sivchari/rask/releases/download/<tag>/rask-bundled-linux-amd64.sha256
sha256sum -c rask-bundled-linux-amd64.sha256
```

## Placement

Drop the verified binary anywhere on the node's persistent data
volume/AMI, `chmod +x` it, and make sure it ends up on `$PATH` (or invoke
it by full path) for whatever process runs `rask create`. That's the whole
placement contract — no directory structure to pre-seed:

- `~/.rask/cache` does not need to exist ahead of time. It self-populates
  from the embedded payload the first time a component is resolved
  (`internal/components.DefaultCache`), entirely offline, with no flag or
  environment variable needed to opt in — a bundled binary behaves this
  way automatically.
- The process running `rask create` needs a writable `$HOME` (rask has no
  `--home-dir` override today; it always resolves state under
  `os.UserHomeDir() + "/.rask"`).
- Linux artifacts are built with `CGO_ENABLED=0` (see the Makefile's
  `bundle` target) — a fully static binary, so there's no libc version to
  match against your base image or Bottlerocket's host userland.

## Privileged requirement (recap)

On Linux, rask runs Kubernetes control-plane and node components as plain
supervised host processes (`internal/substrate/hostproc`) and assumes it
owns the host's network namespace (CNI bridge, kube-proxy iptables rules).
It works inside a container, but only a `--privileged` one — this is
unchanged by bundling; a bundled binary needs exactly the same privileges
an unbundled one does. See the main [README](README.md#requirements) for
the up-to-date statement of this requirement.

## Version-update flow — when to recut the snapshot

A bundled binary's embedded payload is pinned at build time to:

- the Kubernetes version (`components.DefaultK8sVersion`)
- kine, runc, containerd and CNI-plugins versions
  (`internal/components/components.go`'s `KineVersion`/`RuncVersion`/
  `ContainerdVersion`/`CNIPluginsVersion`)

It never re-fetches or auto-updates these at runtime — an old snapshot's
binary keeps working, but keeps provisioning clusters on whatever versions
it was built against until the snapshot is recut with a newer artifact.
Recut when:

1. **A new rask tag bumps any of the pinned versions above.** Check
   `internal/components/components.go` (or the release notes) for a
   version diff before deciding whether a recut is needed for a given
   release — not every rask release changes pinned component versions.
2. **A pinned component gets a security patch** (e.g. a runc or containerd
   CVE fix) — recut immediately on the release that includes it, don't
   wait for the next scheduled cadence.
3. **Periodically, for CA bundle freshness**, on the darwin/arm64 target
   only: `internal/components.EnsureCABundle` normally re-fetches curl.se's
   current CA bundle on every cold cache, but a bundled binary embeds
   whatever was current at `make bundle-payload` time and never refreshes
   it. This doesn't affect the linux/amd64 target haro consumes (it has no
   guest userland — see the platform table above) but is worth knowing if
   this contract is ever extended to a darwin target.

There is no automatic recut trigger from rask's side — this is a pull, not
a push, relationship: haro's pipeline should watch
[GitHub Releases](https://github.com/sivchari/rask/releases) (or `git log`
on `internal/components/components.go`) and decide when to act on a new
artifact.

## What this handoff does not cover

- Container images (CoreDNS, pause, etc.): out of scope for
  `cmd/bundle-payload` today. `internal/components/bundlepayload` has a
  forward-compatible `InstallImages` hook for this, but nothing currently
  populates it — a bundled binary still resolves images the same way an
  unbundled one does.
- Functional verification of the linux/amd64 artifact specifically:
  covered by this repo's own local testing (see `PROGRESS-bundle.md` for
  what was and wasn't verified for this change), not by
  `release-bundle.yaml` itself, which only builds and reports size. Treat
  a new artifact as unverified for your environment until you've run
  `rask create --wait` against it at least once outside this repo's CI.
