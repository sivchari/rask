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

## Quick start

```sh
make codesign   # macOS (build + sign). Linux: make build
rask create cluster --name dev --wait coredns
rask load docker-image myapp:dev --name dev
rask delete cluster --name dev
```

`--context-format 'kind-{name}'` makes tools that trust kind contexts
(e.g. Tilt) work unchanged.

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
