# rask

Disposable local Kubernetes clusters that start in about four seconds.
No kubeadm, no Docker. `rask` is Norwegian for "fast".

| tool | create → node Ready |
|---|---|
| **rask** | **4.0s** (measured p50) |
| k3d | 7-16s |
| kind | 19-20s |

Fast because it skips work instead of doing it faster: control-plane
components run as plain supervised processes (no kubeadm phases, no
containerized node), and `create` copies a prebaked, already-bootstrapped
datastore instead of replaying bootstrap.

## Install

Every release ships one self-contained binary per platform — every
component it needs (Kubernetes binaries, kine, containerd, runc, the CNI
plugins) is embedded at build time, so `rask create` never downloads
anything on first use:

```sh
curl -fLO https://github.com/sivchari/rask/releases/latest/download/rask-bundled_<version>_<os>_<arch>.tar.gz
tar xzf rask-bundled_<version>_<os>_<arch>.tar.gz
chmod +x rask-bundled
```

`<os>_<arch>` is one of `linux_amd64`, `linux_arm64` or `darwin_arm64`.
Verify against the matching `rask_<version>_checksums.txt` /
`rask_<version>_darwin_checksums.txt` release asset before trusting it.

Building from source: `make codesign` (macOS, build + sign) or `make
build` (Linux) produces a plain binary that downloads components on first
use instead — a developer convenience, not a released artifact.

## Quick start

```sh
rask create cluster --name dev --wait coredns
rask load docker-image myapp:dev --name dev
rask delete cluster --name dev
```

`--context-format 'kind-{name}'` makes tools that trust kind contexts
(e.g. Tilt) work unchanged.

To pre-warm `~/.rask/cache` without creating a cluster (e.g. baking a
machine image), run `rask pull` — offline, from the binary's embedded
payload. Only works on a bundled (released) binary; see
[HANDOFF-ebs-bake.md](HANDOFF-ebs-bake.md) for the full bake procedure.

## Requirements

- **macOS**: Apple Silicon. Runs each cluster in a lightweight
  Virtualization.framework VM. arm64 images only.
- **Linux**: runs as host processes, k3s-style — rask assumes it owns the
  host's network (CNI bridge, kube-proxy iptables). Works inside a
  container too, if the container is `privileged`.

## Bring your own Kubernetes

Component binaries, the CoreDNS image, extra apiserver flags, and files
placed before boot are all pluggable — enough to run EKS Distro instead of
upstream:

```sh
rask create cluster --component-dir /path/to/kubernetes-server/bin \
  --apiserver-arg key=value --preboot-file /local/f=dest --coredns-image ref
```

The same options are available as a Go library via
[`pkg/cluster`](pkg/cluster) (see `Example_fjordIntegration`).

## Status

Pre-alpha. CLI and API may change. No addons by design — like kind, you
get a plain cluster and bring your own manifests.

## License

Apache 2.0. See [LICENSE](LICENSE).
