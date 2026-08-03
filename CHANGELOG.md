# Changelog

## v0.1.0 (2026-08-03)

First release.

rask creates single-node Kubernetes clusters by supervising the control-plane
processes directly — no kubeadm, no Docker — reaching node Ready in about 4
seconds on both of its substrates:

- **Linux (hostproc)**: components run as host processes owning the host
  network namespace. Works on a bare host and inside a privileged container.
- **macOS (vz)**: one lightweight Virtualization.framework VM per cluster,
  with userspace networking (no vmnet entitlement needed).

### Features

- `rask create` / `delete` / `list` / `kubeconfig`, with `kubectl exec`,
  `logs`, and `port-forward` fully working against the created cluster
- `rask load docker-image` / `load image-archive`: stream images into the
  cluster's containerd without a registry
- Prebaked cluster-state seed (`rask seed`, auto-used) for faster repeat
  creates; cluster images are prefetched daemonlessly in parallel
- Bundled build (`make bundle`): binaries with the component payload
  embedded, so `rask create` needs no network access
- Go library API (`pkg/cluster`): Create/Delete/List/KubeConfig/LoadImages
- Embedding seams for higher-level tools: `--component-dir` (bring your own
  Kubernetes binaries, e.g. EKS Distro), `--apiserver-arg`, `--preboot-file`,
  `--coredns-image`, `--api-audience`
- Node prerequisites are guaranteed at boot on hostproc: `br_netfilter`
  loaded with `bridge-nf-call-iptables=1`, and kube-proxy configured with
  `conntrack.maxPerCore: 0` so it runs in containers where conntrack sysctls
  are read-only

### Requirements

- Go 1.26+ to build; Linux needs root (hostproc owns host networking)
- macOS 13+ on Apple silicon for the vz substrate
