# rask

Disposable local Kubernetes clusters that start in about four seconds.

`rask` is Norwegian for "fast".

- **No kubeadm.** Control-plane components are launched directly as supervised
  processes, so there is no bootstrap phase to replay.
- **No Docker dependency.** A native Virtualization.framework VM on macOS, plain
  host processes on Linux.
- **Prebaked datastore.** `create` copies an already-bootstrapped state file
  instead of writing one from scratch.
- **Pluggable component binaries.** Run upstream Kubernetes, or point rask at a
  directory of your own — EKS Distro, a local build, anything laid out like
  `kubernetes-server/bin`.

## Speed

Measured on this project's benchmark harness (`test/benchmark/`), timing
`rask create cluster --wait=node` end to end:

| tool | node Ready | source |
|---|---|---|
| **rask** | **4.0s** (p50), 4.3s (p95) | measured, `test/benchmark/RESULTS-linux.md` |
| k3d | 7-16s | published figures |
| microk8s | ~12.5s | published figures |
| kind | 19-20s | kind maintainer, kind#845 |
| minikube (docker driver) | 29s | published figures |

That is roughly 5x faster than kind, and faster than k3d's entire published
range. On macOS the same command measures 4.6s (p50), the extra time being
Virtualization.framework VM startup.

The cluster's own internal timeline — kernel/process start through node Ready —
is about 3.1s; the rest is process launch and teardown around it.

## Status

Pre-alpha. The API and CLI may still change.

| milestone | state |
|---|---|
| M0 — feasibility spikes | done |
| M1 — macOS (Virtualization.framework) | done |
| M2 — third-party operator E2E | done |
| M3 — running inside a container | done, requires `privileged: true` |

M3's finding is worth stating plainly: rask works inside a container at the same
speed as on a bare host, but only when that container is privileged. A
non-privileged container with added capabilities is not sufficient.

## Requirements

**macOS** — Apple Silicon. The binary must be codesigned with the
Virtualization.framework entitlement (`make codesign` does this). Only arm64
images run; there is no x86 emulation.

**Linux** — rask does not isolate the cluster in its own network namespace. Like
k3s, it assumes it owns the host's: the CNI bridge, the kube-proxy iptables
rules and the node IP are the host's own.

## Build

```sh
make build      # Linux
make codesign   # macOS: build, then sign with vz.entitlements
```

`make build` first cross-compiles `rask-init`, the guest-side init embedded into
the binary, so build it through the Makefile rather than `go build` directly.

## Usage

```sh
rask create cluster --name dev --wait coredns
rask get clusters
rask export kubeconfig --name dev --context-format 'kind-{name}'
rask load docker-image myapp:dev --name dev
rask delete cluster --name dev
```

`--wait` takes `node` (default) or `coredns`. `--verbose` prints a phase-by-phase
boot latency breakdown.

`--context-format` is a global flag controlling the kubeconfig context name;
`{name}` is replaced with the cluster name. It defaults to `rask-{name}`, and
setting it to `kind-{name}` lets tools that recognise clusters by context name —
Tilt, for one — work against rask without configuration.

rask deliberately ships no addons: like kind, it gives you a plain cluster and
you bring your own manifests.

### Bringing your own components

```sh
rask create cluster \
  --component-dir /path/to/kubernetes-server/bin \
  --coredns-image my-registry/coredns:v1.11.4 \
  --preboot-file /local/webhook.yaml=auth/webhook.yaml \
  --apiserver-arg authentication-token-webhook-config-file=/…/preboot/auth/webhook.yaml
```

`--preboot-file` places a file into the cluster *before* the control plane
starts, which is what makes flags like the one above usable: the apiserver needs
the file to exist by the time it reads its own arguments.

## Go API

`pkg/cluster` drives the same machinery as the CLI.

```go
provider, err := cluster.NewProvider("")
if err != nil {
	return err
}

result, err := provider.Create(ctx, "dev", cluster.Options{
	Wait:         cluster.WaitCoreDNS,
	ComponentDir: "/path/to/kubernetes-server/bin",
})
if err != nil {
	return err
}

fmt.Println(result.KubeconfigPath)
```

A preboot file's absolute destination is
`<cluster-data-dir>/preboot/<dest>`, and the cluster data directory is
derivable from `provider.KubeConfigPath(name)` — so a caller can compute the
path to a file it is about to install, and reference it in an apiserver
argument, before `Create` ever runs. See `Example_fjordIntegration` in
`pkg/cluster` for the whole shape.

## License

Apache 2.0. See [LICENSE](LICENSE).
