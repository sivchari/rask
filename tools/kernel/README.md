# rask guest kernel

A reproducible pipeline that produces an uncompressed arm64 Linux `Image`
purpose-built for rask's vz guests: every feature a single-node Kubernetes
node needs is built in (`=y`, no loadable modules), so `rask-init` never has
to load a kernel module at boot.

## Status: build moved to CI, not yet produced or boot-validated

The pipeline (`Dockerfile.build`, `rask.config`, `build.sh`) is complete and
was exercised locally against colima's docker context (aarch64 native VM on
Apple Silicon) as a native `make ARCH=arm64 Image` build. **The local build
crashed dockerd itself, twice**, partway through compiling `vmlinux`
(observed failure: `error waiting for container: unexpected EOF`, docker
`BUILD EXIT: 125`) -- colima's default VM here is 2 vCPU / 16GiB, and a
parallel kernel compile (many `cc1` processes + ccache) is memory/IO-hungry
enough to take the whole docker daemon down with it, not just the build
container. Retrying locally would repeat the same failure and risks the
user's other long-running containers on that same colima VM (kind clusters,
postgres, temporal, etc. observed running alongside it).

**Decision: build exclusively in CI** (`.github/workflows/kernel.yaml`), on
a dedicated, disposable, adequately-resourced runner, never on the shared
local colima VM again. See "CI build" below.

Because no `Image` was produced, **the boot-time validation against puipui
(S2 harness) and the S3 Rosetta/containerd end-to-end flow have not been
run yet**. Do not treat any number in this document as a measured result
until a CI-built `Image` has actually gone through both harnesses -- update
this README with real S2/S3 output at that point (see "Validation, once an
Image exists" below).

### v1 production note

Until a CI-built rask kernel has been boot-validated, **rask's v1
`internal/substrate/vz` should ship with the Alpine `linux-virt` 6.6.142-0-virt
kernel + `rask-init`'s module-loading path**, exactly as proven end-to-end
during the M0 s3 spike (containerd ready in 41ms, amd64 container start
via Rosetta in ~70-90ms, `RASK-S3-ALL-DONE` with `uname=x86_64` in 3/3 runs).
That is a real, validated kernel+boot-path today; this tools/kernel/
pipeline is the built-in-features replacement for it and should only
replace the Alpine kernel in production once it has passed the same S2/S3
validation the Alpine kernel already has.

## Kernel version pinned

**6.6.142** -- same version as the Alpine `linux-virt` kernel the M0 s3
spike validated (cross-checked against Alpine's actual
`config-6.6.142-0-virt`, examined during that spike -- see the M0 spike
results in git history), so the config fragment below could be
cross-checked line-by-line against a config that is *known* to boot under
vz and run a full containerd/Rosetta flow, not just against defconfig
defaults. Override via `KERNEL_VERSION` env var to `build.sh` if a later
6.6.x LTS point release is needed.

## Pipeline

```
tools/kernel/
  Dockerfile.build   native arm64 build container: debian bookworm +
                     build-essential, bc, bison, flex, libssl-dev,
                     libelf-dev, ccache, curl, xz-utils
  rask.config        curated kconfig fragment (see rationale below) --
                     single source of truth, in git
  build.sh           orchestrates: build the container image, download +
                     cache linux-<version>.tar.xz from cdn.kernel.org
                     (resumable: `curl -C -` against a `.partial` file),
                     extract it into a named docker volume
                     (`rask-kernel-src`, not a bind mount -- virtiofs bind
                     mounts on colima were both slow for a tree this size
                     and mangled tar's permission/ownership restores),
                     `make ARCH=arm64 defconfig`, merge rask.config via
                     scripts/kconfig/merge_config.sh -m, `make olddefconfig`
                     (fails the build if any fragment value didn't survive,
                     see Config rationale below), `make Image` (never `make
                     modules` -- nothing is ever built as a loadable
                     module), copy the result out
  work/              gitignored: downloaded tarball, ccache, build.log,
                     final Image + config-used (the resolved .config
                     actually used) + kernelversion -- populated by
                     build.sh (the extracted source tree itself lives in
                     the `rask-kernel-src` docker volume, not under work/)
```

`build.sh` is idempotent: re-running it skips the download if
`work/src/linux-<version>.tar.xz` already exists and skips extraction if
the `rask-kernel-src` volume already has `linux-<version>/`, and ccache
(`work/ccache/`) speeds up recompiles after a config tweak.

## Config rationale (`rask.config`)

Applied on top of `make ARCH=arm64 defconfig`. Grouped exactly as the task
brief specified, cross-referenced against two sources per line:

1. **Alpine's actual `.config` for the linux-virt 6.6.142-0-virt kernel**
   (examined during the M0 s3 spike -- see the M0 spike results in git
   history) -- the one that spike booted under vz and ran a real containerd
   + Rosetta + amd64-image flow against. Every symbol in that config which
   was `=m` and is needed for boot or container-runtime operation is
   flipped to `=y` here (Alpine ships it as a module because it's a
   general-purpose distro kernel; rask never loads modules).
2. **The M0 s3 spike's "production recipe" module list** -- the exact
   modules `rask-init` loaded by hand in that spike (rng-core, virtio-rng,
   af_packet, failover, net_failover, virtio_net, fuse, virtiofs,
   binfmt_misc, overlay) -- confirms nothing in that list was missed.

Categories:

- **virtio, all `=y`**: `VIRTIO`, `VIRTIO_PCI(+_LEGACY)` (vz uses the
  virtio-pci transport, not virtio-mmio -- confirmed because Alpine's
  config has `VIRTIO_MMIO=m` unused and still boots fine under vz per S3;
  `VIRTIO_MMIO` is left out of the fragment entirely), `VIRTIO_BLK`,
  `VIRTIO_NET` (+`FAILOVER`/`NET_FAILOVER`, matching the module load order
  in S3), `VIRTIO_CONSOLE`, `VIRTIO_FS` + `FUSE_FS`, `VIRTIO_VSOCKETS`(+`_COMMON`)
  + `VSOCKETS`, `HW_RANDOM` + `HW_RANDOM_VIRTIO` (S3: guest RNG starvation
  hangs DHCP for 2 minutes without this).
- **Rosetta**: `BINFMT_MISC`, `BINFMT_ELF`, `BINFMT_SCRIPT`.
- **filesystems**: `EXT4_FS`(+posix acl/security), `EROFS_FS`(+posix
  acl/security/xattr -- not present at all in Alpine's config, added purely
  from the task brief since Alpine's `linux-virt` doesn't ship it),
  `OVERLAY_FS` (containerd snapshotter), `TMPFS`(+posix acl/xattr/inode64).
- **namespaces, full set**: `NAMESPACES`, `PID_NS`, `NET_NS`, `UTS_NS`,
  `IPC_NS`, `USER_NS`.
- **cgroups v2, complete controller set**: `CGROUPS`, `CGROUP_SCHED` +
  `FAIR_GROUP_SCHED` + `CFS_BANDWIDTH` (kubelet CPU quota/CFS bandwidth),
  `CGROUP_PIDS`, `CGROUP_CPUACCT`, `CPUSETS`, `CGROUP_FREEZER`,
  `CGROUP_DEVICE`, `CGROUP_HUGETLB`, `MEMCG`+`MEMCG_KMEM`, `BLK_CGROUP`,
  plus `CGROUP_BPF`/`CGROUP_PERF`/`CGROUP_NET_CLASSID`/`CGROUP_NET_PRIO`/
  `CGROUP_WRITEBACK` (all `=y` in Alpine's validated config, kept for
  parity).
- **netfilter/iptables for kube-proxy (iptables mode) + CNI bridge+portmap**:
  `NETFILTER`, `NETFILTER_XTABLES`, `NF_CONNTRACK`(+`_MARK`/`_EVENTS`),
  `NF_NAT`(+`_MASQUERADE`/`_REDIRECT`), `NETFILTER_XT_NAT`, the exact xt
  match/target set the task listed -- `comment`, `conntrack`, `addrtype`,
  `mark`, `multiport`, `state`, `MASQUERADE`, `REDIRECT`, `physdev` -- plus
  `IP_NF_IPTABLES` (filter/nat/mangle/raw tables + MASQUERADE/REDIRECT
  targets), `IP6_NF_IPTABLES` for dual-stack,
  `BRIDGE`+`BRIDGE_NETFILTER`+`NETFILTER_FAMILY_BRIDGE`, `VETH`.
  **`CONFIG_IPV6=y` is required here too**, even though it isn't in the
  task's netfilter list: `BRIDGE` and `IP6_NF_IPTABLES` both have Kconfig
  deps of the shape `depends on IPV6` (bridge's is the classic `depends on
  IPV6 || IPV6=n` guard), and arm64 `defconfig` defaults `IPV6=m`; a
  built-in (`=y`) symbol cannot depend on a module, so without forcing
  `IPV6=y` the whole bridge/ip6tables/physdev chain silently gets capped
  back to `=m` by `olddefconfig` despite the fragment requesting `=y`. Two
  symbols were dropped after this investigation because they're not
  independently settable or not actually required: `NETFILTER_NETLINK` has
  no Kconfig prompt at all (it's a `select`-only glue symbol, computed from
  whatever needs it -- not needed for kube-proxy's non-netlink iptables
  path anyway), and `NF_CONNTRACK_SECMARK` depends on `NETWORK_SECMARK`
  (SELinux connection labeling) which nothing in the k8s networking path
  requires.
- **misc kubelet/runtime needs**: `PACKET` (AF_PACKET), `INOTIFY_USER`,
  `POSIX_MQUEUE`.
- **aggressively disabled** (task: "no sound/GPU/wireless/USB etc"):
  `SOUND`, `DRM`, `FB`, `USB_SUPPORT`, `WLAN`, `CFG80211`, `BT`,
  `MEDIA_SUPPORT`, `MTD`, `INFINIBAND`, `STAGING`. `CONFIG_MODULES` itself
  is left at the defconfig default (`=y`) per the task brief -- the pipeline
  simply never runs `make modules` or packages any `.ko`, so it stays
  "present but empty."

`build.sh` runs a mismatch check after `merge_config.sh` + `olddefconfig`:
every fragment line is grepped against the final `.config`, and anything
that didn't stick (unmet Kconfig dependency) is printed as `MISMATCH: ...`
in the build log so a config problem is caught immediately instead of
discovered at boot time. **This check found 9 real mismatches** on the
first local attempt (`NETFILTER_NETLINK`, `NF_CONNTRACK_SECMARK`,
`NETFILTER_XT_MATCH_PHYSDEV`, `IP6_NF_IPTABLES`/`_FILTER`/`_NAT`/`_MANGLE`,
`BRIDGE`, `BRIDGE_NETFILTER`) -- all traced to the missing `IPV6=y` (or, for
the first two, symbols that were never independently settable/required, see
above) and fixed in `rask.config`. Verified clean with a **config-only dry
run** (`defconfig` + `merge_config.sh` + `olddefconfig`, no compile --
cheap, seconds, does not touch `make Image`/the crash-prone compile step):
`ALL FRAGMENT VALUES HONORED`, zero mismatches, against 6.6.142 source
already cached in `work/src/`.

## CI build

`.github/workflows/kernel.yaml`: `workflow_dispatch` (build on demand) +
runs automatically on `push`/`pull_request` when `tools/kernel/**` changes.

Primary path: `runs-on: ubuntu-24.04-arm`, a GitHub-hosted native arm64
runner (free for public repos as of the runner images that ship
`ubuntu-24.04-arm`/`ubuntu-22.04-arm`; a paid arm64 runner type on
Team/Enterprise for private repos). This runs `tools/kernel/build.sh`
completely unmodified -- native compile, no cross toolchain, and a
dedicated ephemeral VM instead of a shared local one, so the dockerd-crash
failure mode seen locally should not recur (more RAM headroom, nothing else
competing for the daemon).

If arm64 hosted runners are not available for this repo, the fallback is an
`ubuntu-latest` (x86_64) runner cross-compiling:

```sh
apt-get install -y gcc-aarch64-linux-gnu
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- defconfig
./scripts/kconfig/merge_config.sh -m -O . .config rask.config
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- olddefconfig
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- -j"$(nproc)" Image
```

i.e. the same three `make` invocations `build.sh` already does inside the
container, with `CROSS_COMPILE=aarch64-linux-gnu-` added and `CC="ccache
gcc"` swapped for `CC="ccache aarch64-linux-gnu-gcc"`. Not implemented as a
separate script yet since the arm64-runner path is simpler and was picked
as primary; do this only if `ubuntu-24.04-arm` turns out to be unavailable
for this repo.

The workflow uploads `work/Image` + `work/config-used` as a build artifact
and verifies the uncompressed-arm64 magic (`"ARMd"` at offset `0x38`)
before doing so.

## Validation, once an Image exists

Not yet run. Once `tools/kernel/kernel.yaml` produces an artifact, download
it to `tools/kernel/work/Image` and:

1. **Magic check**: bytes at offset `0x38` should read `ARMd` (CI already
   gates on this, but re-check locally too).
2. **S2 harness** (boot-to-init latency vs puipui's 125ms p50 baseline).
   The `spike-s2` harness and its `RESULTS.md` were removed along with the
   M0 spikes in commit 75816cd; check out the tree at that commit's parent
   to rebuild and run it:
   ```sh
   git checkout 75816cd~1 -- spikes/s2
   cd spikes/s2
   ./work/spike-s2 -kernel /abs/path/to/tools/kernel/work/Image \
     -initrd work/initramfs.cpio -runs 10
   ```
   Target: within 2x of puipui's 125ms p50 (i.e. under ~250ms) is
   acceptable per the task brief; a much larger built-in feature set
   (netfilter, cgroups, virtio-fs/vsock, etc. vs puipui's bare virtio-console/
   net/vsock) is expected to cost some boot time over puipui specifically,
   but should still be far under the 2s spike target from S2.
3. **S3 harness** (Rosetta/containerd end-to-end, modules expected to
   fail-open since this Image ships no `/lib/modules/*.ko.gz` -- the s3
   spike's `init/modules.go` + `main.go` already handled a missing module
   file by printing `RASK-S3-MODULES-FAILED` and continuing, no spike code
   changes needed). Like S2, the `spike-s3` harness was removed with the M0
   spikes in commit 75816cd; check out the tree at that commit's parent to
   rebuild and run it:
   ```sh
   git checkout 75816cd~1 -- spikes/s3
   cd spikes/s3
   ./work/spike-s3 -kernel /abs/path/to/tools/kernel/work/Image \
     -initrd work/initramfs.cpio
   ```
   Target: `RASK-S3-ALL-DONE` with `uname=x86_64`, matching the Alpine
   kernel's 3/3 pass rate the M0 s3 spike recorded.
4. Update this README's "Status" section with the real kernel version,
   `Image` size, measured S2 boot time (p50/p95), and S3 pass/fail --
   replacing this whole section once done.

## Known gaps / follow-ups

- No CI run has happened yet -- `.github/workflows/kernel.yaml` is
  untested (drafted per repo convention from `.github/workflows/ci.yaml`
  but not exercised).
- The config fragment is verified consistent (`ALL FRAGMENT VALUES
  HONORED` on a config-only dry run) but **the actual compile
  (`make Image`) has never completed** -- a clean config doesn't guarantee
  a clean build (e.g. a source-level build error independent of Kconfig).
  First CI run should be watched end-to-end, not assumed to pass.
- PCI host-controller Kconfig symbols (`PCI_HOST_GENERIC`, `PCI_ECAM`,
  etc.) were deliberately left untouched (relying on `defconfig`'s
  defaults) rather than hand-curated, since Alpine's validated config gets
  them from the same defconfig baseline; if CI reveals vz needs a specific
  PCI host driver defconfig doesn't enable by default, add it to
  `rask.config` explicitly.
- `ubuntu-24.04-arm` runner availability for this specific repo was not
  verified (no access to check GitHub org/repo settings from this
  environment) -- if the first `kernel.yaml` run fails to find that runner
  label, fall back to the x86_64 cross-compile recipe documented above.
