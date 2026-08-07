# Changelog

## [v0.4.2](https://github.com/sivchari/rask/compare/v0.4.1...v0.4.2) - 2026-08-07
- fix(vz): load xfrm_user and make the guest root rshared by @sivchari in https://github.com/sivchari/rask/pull/33

## [v0.4.1](https://github.com/sivchari/rask/compare/v0.4.0...v0.4.1) - 2026-08-07
- feat(hostproc): detect the two in-container failures that fail unintelligibly by @sivchari in https://github.com/sivchari/rask/pull/31

## [v0.3.2](https://github.com/sivchari/rask/compare/v0.3.1...v0.3.2) - 2026-08-07
- feat(cli): add rask get preboot-path by @sivchari in https://github.com/sivchari/rask/pull/29

## [v0.3.1](https://github.com/sivchari/rask/compare/v0.3.0...v0.3.1) - 2026-08-07
- fix(rask-init): create /tmp so kubectl exec works in vz guests by @sivchari in https://github.com/sivchari/rask/pull/27

## [v0.2.2](https://github.com/sivchari/rask/compare/v0.2.1...v0.2.2) - 2026-08-07
- feat(pkg/cluster): expose Provider.PortForward by @sivchari in https://github.com/sivchari/rask/pull/25

## [v0.2.1](https://github.com/sivchari/rask/compare/v0.2.0...v0.2.1) - 2026-08-06
- fix(bundle-payload): stop cross-target blob leakage into bundled binaries by @sivchari in https://github.com/sivchari/rask/pull/23

## [v0.1.10](https://github.com/sivchari/rask/compare/v0.1.9...v0.1.10) - 2026-08-06
- feat: bundle the cluster's container images into the payload by @sivchari in https://github.com/sivchari/rask/pull/21

## [v0.1.9](https://github.com/sivchari/rask/compare/v0.1.8...v0.1.9) - 2026-08-06
- feat(cluster): resolve preboot paths per substrate by @sivchari in https://github.com/sivchari/rask/pull/19

## [v0.1.8](https://github.com/sivchari/rask/compare/v0.1.7...v0.1.8) - 2026-08-06
- feat(cluster): make the Go library usable on macOS by @sivchari in https://github.com/sivchari/rask/pull/17

## [v0.1.7](https://github.com/sivchari/rask/compare/v0.1.6...v0.1.7) - 2026-08-06
- feat(vz): fail fast when the binary lacks the virtualization entitlement by @sivchari in https://github.com/sivchari/rask/pull/15

## [v0.1.6](https://github.com/sivchari/rask/compare/v0.1.5...v0.1.6) - 2026-08-06
- feat: ship bundled-only, content-address the caches, add rask pull by @sivchari in https://github.com/sivchari/rask/pull/13

## [v0.1.5](https://github.com/sivchari/rask/compare/v0.1.4...v0.1.5) - 2026-08-06
- feat(vz): implement PortForward through the guest agent by @sivchari in https://github.com/sivchari/rask/pull/10
- fix(vz): ship a real rask-init instead of the placeholder by @sivchari in https://github.com/sivchari/rask/pull/12

## [v0.1.4](https://github.com/sivchari/rask/compare/v0.1.3...v0.1.4) - 2026-08-06
- feat(vz): macOS parity — apiserver args, CoreDNS image, component dir, image prefetch by @sivchari in https://github.com/sivchari/rask/pull/8

## [v0.1.3](https://github.com/sivchari/rask/compare/v0.1.2...v0.1.3) - 2026-08-03
- fix: readiness error keeps the real cause when the deadline fires mid-request by @sivchari in https://github.com/sivchari/rask/pull/5
- ci: stop parallel goreleaser runs from splitting one tag into two releases by @sivchari in https://github.com/sivchari/rask/pull/7

## [v0.1.2](https://github.com/sivchari/rask/compare/v0.1.1...v0.1.2) - 2026-08-03
- ci: release via goreleaser by @sivchari in https://github.com/sivchari/rask/pull/3
- ci: darwin release via parallel goreleaser config by @sivchari in https://github.com/sivchari/rask/pull/4

## [v0.1.1](https://github.com/sivchari/rask/compare/v0.1.0...v0.1.1) - 2026-08-03

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
