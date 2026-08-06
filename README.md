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
plugins) **and** every container image its default manifest bundle needs
(CoreDNS, the CRI pause image, local-path-provisioner) is embedded at build
time, so `rask create` never downloads anything on first use, not even a
registry pull:

```sh
curl -fLO https://github.com/sivchari/rask/releases/latest/download/rask-bundled_<version>_<os>_<arch>.tar.gz
tar xzf rask-bundled_<version>_<os>_<arch>.tar.gz
chmod +x rask-bundled
```

`<os>_<arch>` is one of `linux_amd64`, `linux_arm64` or `darwin_arm64`.
Verify against the matching `rask_<version>_checksums.txt` /
`rask_<version>_darwin_checksums.txt` release asset before trusting it.

A rask release pins its Kubernetes version, CoreDNS, the CRI pause image
and local-path-provisioner together — upgrading rask upgrades all of them
at once, there is no independent "image version" to track. See
[HANDOFF-ebs-bake.md](HANDOFF-ebs-bake.md) for the full contract.

Building from source: `make codesign` (macOS, build + sign) or `make
build` (Linux) produces a plain binary that downloads components on first
use instead — a developer convenience, not a released artifact. On macOS,
a source build must be signed with the `com.apple.security.virtualization`
entitlement (`make codesign` does this) — released binaries are already
signed; a plain `go build`/`make build` is not, and fails fast on `rask
create` with instructions to fix it.

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

## Using rask as a library

`pkg/cluster` is rask's importable Go API — the library form of `rask
create/delete/get/export`, for a program (e.g. fjord) that wants to drive
rask in-process instead of shelling out to the `rask` binary. On macOS,
your own binary — not `rask` — is what actually hosts each cluster's VM, so
it must do three things:

1. **Call the re-exec entrypoint first.** Each cluster's VM runs in a
   detached process that outlives the call that created it, spawned by
   re-execing the currently running binary. Call `cluster.RunVMHostIfRequested()`
   as the very first line of `main`, before your own flag/subcommand
   parsing — without it, that re-exec has no matching entrypoint, and
   `Provider.Create` fails only after a boot timeout instead of ever
   getting the chance to host the VM. Safe to call unconditionally: it
   returns `(false, nil)` immediately for an ordinary invocation, and
   compiles to a no-op on Linux.

   ```go
   func main() {
       if handled, err := cluster.RunVMHostIfRequested(); handled {
           if err != nil {
               log.Fatal(err)
           }
           return
       }
       // ... the rest of your program's normal startup
   }
   ```

2. **Cross-compile and embed rask-init at build time.** A module
   consumer's own copy of rask's embedded `rask-init` binary is a
   placeholder (it has to be, so `go build` works from a read-only module
   cache), so supply a real one via `cluster.WithRaskInit`:

   ```sh
   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o internal/embedded/rask-init github.com/sivchari/rask/cmd/rask-init
   ```

   ```go
   //go:embed internal/embedded/rask-init
   var raskInit []byte

   provider, err := cluster.NewProvider(homeDir, cluster.WithRaskInit(raskInit))
   ```

3. **Codesign your own binary.** It hosts the VM, so it — not `rask` —
   needs the `com.apple.security.virtualization` entitlement:
   `codesign --entitlements vz.entitlements -f -s - your-binary` (see
   `make codesign` for the equivalent used to sign released `rask`
   binaries). rask already fails fast with fix instructions if this is
   missing.

See `Example_fjordIntegration` in [`pkg/cluster`](pkg/cluster) for the full
option set beyond these three requirements.

## Status

Pre-alpha. CLI and API may change. No addons by design — like kind, you
get a plain cluster and bring your own manifests.

## License

Apache 2.0. See [LICENSE](LICENSE).
