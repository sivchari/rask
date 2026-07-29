# rask

Disposable local Kubernetes clusters, an order of magnitude faster than kind.

- No kubeadm: components are launched directly as supervised processes
- No Docker dependency: native Virtualization.framework VM on macOS, host processes on Linux
- Prebaked datastore: cluster create copies a bootstrapped state file instead of replaying bootstrap
- amd64 images run on Apple Silicon via Rosetta, as a first-class feature
- Pluggable Kubernetes component binaries (upstream or EKS Distro)

`rask` is Norwegian for "fast".

Status: pre-alpha, milestone M0 (feasibility spikes).
