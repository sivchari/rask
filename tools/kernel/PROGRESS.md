# rask guest kernel build — progress log

Task: build a production arm64 Linux Image (uncompressed, =y everything, no
modules) for rask's vz guests. See spikes/s3/RESULTS.md, spikes/s2/RESULTS.md,
research-m0-spikes.md for requirements context.

## Environment discovered
- colima VM: aarch64 native (macOS host is Apple Silicon), docker context
  `colima`, 2 vCPU / 16GiB configured for the VM. Network egress from
  containers works fine (cdn.kernel.org reachable); direct host `curl` is
  slow/flaky in this sandbox — always issue network calls *inside* docker
  containers, not from host bash.
- spikes/s3/work/altkernel/boot/config-6.6.142-0-virt is Alpine's actual
  linux-virt 6.6.142-0-virt .config — the exact kernel S3 validated
  end-to-end (containerd + Rosetta + amd64 image run). Using it as the
  primary reference for which symbols must be on, converting its virtio/
  netfilter/namespace/cgroup =m entries to =y per the task's "no modules"
  requirement.
- spikes/s3/init/modules.go + main.go: `loadBootModules()` already fails
  open (prints RASK-S3-MODULES-FAILED and continues) if /lib/modules/*.ko.gz
  are missing — confirmed by reading main.go lines 34-39. So pointing the S3
  harness at our new Image (which ships no module files) needs NO changes to
  spike code; module loading will just no-op/fail-open as the task predicted.
- S2 harness: `spikes/s2/work/spike-s2 -kernel <path> -initrd work/initramfs.cpio -runs N`
  (cwd spikes/s2). S3 harness: `spikes/s3/spike-s3` (built from spikes/s3/main.go,
  not yet built as of task start — check spikes/s3/work/spike-s3) same flag shape.

## Plan
1. [ ] tools/kernel/Dockerfile.build — debian bookworm + build-essential/bc/
   bison/flex/libssl-dev/libelf-dev/ccache/curl/xz, native arm64 (no cross
   toolchain needed, colima VM is arm64).
2. [ ] tools/kernel/rask.config — curated fragment (defconfig + merge_config.sh).
3. [ ] tools/kernel/build.sh — orchestrates docker build/run, downloads
   linux-6.6.142.tar.xz from cdn.kernel.org, caches in work/src, builds
   Image only (not `make modules`), ccache in work/ccache.
4. [ ] Run build in background, poll.
5. [ ] Validate Image magic (ARM64 magic at 0x38), boot with S2 harness,
   compare to puipui's 125ms baseline.
6. [ ] Boot with S3 harness (-kernel flag), confirm RASK-S3-ALL-DONE /
   uname=x86_64 still passes (modules fail-open expected/acceptable).
7. [ ] tools/kernel/README.md.

## Status
- tools/kernel/Dockerfile.build, rask.config, build.sh written. rask.config
  is a curated fragment cross-checked line-by-line against
  spikes/s3/work/altkernel/boot/config-6.6.142-0-virt (Alpine's real
  6.6.142-0-virt .config, the exact kernel S3 validated) converting the
  virtio/cgroup/netfilter/namespace =m entries needed for a k8s node to =y,
  plus EROFS_FS (absent from Alpine's config, added per task brief) and
  explicit disables for SOUND/DRM/FB/USB/WLAN/BT/MEDIA/MTD/INFINIBAND/STAGING.
  PCI transport confirmed correct choice (not MMIO): Alpine's config has
  VIRTIO_PCI=y / VIRTIO_MMIO=m and boots fine under vz per S3, so vz uses
  virtio-pci.
- .gitignore: added `/tools/kernel/work/` (source tarball + ccache + output
  live there, matches task instruction to gitignore it).
- Confirmed spikes/s2/work/spike-s2 and spikes/s3/work/spike-s3 are both
  built, codesigned with com.apple.security.virtualization entitlement, and
  ready to run with `-kernel <path>` pointing at our new Image (both already
  take `-kernel`/`-initrd` flags, default `work/Image` relative to their own
  spike dir — pass an absolute path to point at tools/kernel/work/Image).
  spikes/s3/init/modules.go + main.go confirmed to fail-open on missing
  module files already (no spike code changes needed).
- Build launched in background via `nohup ./build.sh > work/build.log 2>&1 &`
  (NOT via the harness's run_in_background bash option — that mechanism
  seems to lose the work/ directory between calls in this sandbox, so a
  detached nohup process was used instead; a Monitor watch was armed to
  grep the log for completion/error/mismatch markers).
- 11:58 checkpoint: downloading linux-6.6.142.tar.xz from cdn.kernel.org
  inside the builder container, ~31%/134MB at ~600KB/s (~2-3min ETA for the
  download alone). Builder image (debian bookworm + build-essential/bc/
  bison/flex/libssl-dev/libelf-dev/ccache/curl/xz-utils) built successfully.
  Process confirmed alive via `ps aux` (docker run curl still running).
  Colima VM is 2 vCPU / 16GiB — expect the actual `make Image` compile
  (native arm64, ccache-backed) to be the long pole after the download+
  extract finish.

## Next steps (resume point if this session is compacted/restarted)
1. Poll tools/kernel/work/build.log for `^==> done:` (success) or
   `make.*Error`/`fatal:`/`MISMATCH:` (failure/config problem). Do NOT
   re-launch build.sh if a build is already in flight — check `ps aux | grep
   build.sh` first, since it's idempotent-safe to resume but wasteful to
   restart mid-compile.
2. On success: tools/kernel/work/Image will exist. Verify ARM64 magic
   ("ARMd" at offset 0x38) — build.sh already prints this check at the end
   of its log.
3. Boot validation:
   - S2: `cd spikes/s2 && ./work/spike-s2 -kernel <abs path to new Image>
     -initrd work/initramfs.cpio -runs 5` — compare t2 p50 against puipui's
     125ms baseline (2x = 250ms budget).
   - S3: `cd spikes/s3 && ./work/spike-s3 -kernel <abs path to new Image>
     -initrd work/initramfs.cpio` (check spike-s3's actual flag names/cwd
     assumptions in main.go before running — verify initrd default path).
     Confirm RASK-S3-ALL-DONE with uname=x86_64 in the output; modules
     failing to load is expected/acceptable (fail-open confirmed).
4. Write tools/kernel/README.md with real kernel version, Image size,
   measured boot times, S3 pass/fail, and any config additions made beyond
   the original list (e.g. if merge_config MISMATCH output revealed missing
   deps that needed extra fragment lines).

## PIVOT (coordinator directive): local build abandoned, moved to CI
The local `make Image` compile crashed dockerd itself, twice (confirmed:
`error waiting for container: unexpected EOF`, `BUILD EXIT: 125`, mid-way
through compiling core kernel objects). colima's VM here is 2 vCPU/16GiB,
shared with the user's other long-running containers (kind clusters,
postgres, temporal, etc.) -- a parallel kernel compile is memory/IO-hungry
enough to take the daemon down with it. Coordinator instructed: stop
retrying the full build locally, pivot to building in GitHub Actions, and
document a v1 fallback (ship the Alpine kernel S3 already validated).

Work done after the pivot:
1. Stopped/removed 2 leftover docker containers from diagnostic dry-runs
   (`amazing_blackwell`, `angry_merkle`, image `rask-kernel-builder`) --
   these were NOT the crashed build itself (that container already died on
   its own) but orphaned config-only dry-run containers that stayed "Up"
   longer than expected. Removed the cached `rask-kernel-builder` image
   too. Did not touch colima or any of the user's unrelated containers
   (fjord-lb-control-plane, flagfield-control-plane, haro-local-control-plane,
   postgres-haro, redis) -- verified untouched via `docker ps` before/after.
2. **Used the already-downloaded+extracted kernel source
   (`work/src/linux-6.6.142/`) to run a config-only dry run** (`make
   defconfig` + `merge_config.sh` + `olddefconfig`, NOT `make Image` --
   seconds, no memory pressure, does not repeat the crash-causing
   operation) to actually diagnose the 9 `MISMATCH:` lines the crashed
   build's log had already surfaced before it died. Root cause: arm64
   defconfig defaults `CONFIG_IPV6=m`, and both `CONFIG_BRIDGE` (`depends
   on IPV6 || IPV6=n`, the classic "can't have a builtin depend on a
   module" guard) and `CONFIG_IP6_NF_IPTABLES` (`depends on INET && IPV6`)
   were therefore silently capped back to `=m` by `olddefconfig`, cascading
   to `BRIDGE_NETFILTER` and `NETFILTER_XT_MATCH_PHYSDEV`. Fixed by adding
   `CONFIG_IPV6=y` to rask.config. Also dropped two lines that were never
   fixable/needed: `NETFILTER_NETLINK` (no Kconfig prompt, `select`-only,
   not required by kube-proxy's non-netlink iptables path) and
   `NF_CONNTRACK_SECMARK` (depends on `NETWORK_SECMARK`/SELinux labeling,
   not part of k8s networking). Re-ran the dry run: `ALL FRAGMENT VALUES
   HONORED`, zero mismatches.
3. Wrote `.github/workflows/kernel.yaml` (workflow_dispatch + push/PR on
   `tools/kernel/**` changes, `runs-on: ubuntu-24.04-arm` native hosted
   runner, runs `build.sh` unmodified, verifies the ARM64 Image magic,
   uploads the Image as an artifact). Only this one file was added under
   `.github/` (left `.github/workflows/ci.yaml` untouched, per boundary
   with the M1 agent).
4. Rewrote `tools/kernel/README.md` around the CI-first design: status
   section explains the local crash and pivot, v1 production note says to
   ship the Alpine kernel (S3-validated) until a CI-built rask kernel
   passes S2/S3, full config rationale (now including the IPV6 finding),
   CI build section (primary: arm64 hosted runner; documented fallback:
   x86_64 + gcc-aarch64-linux-gnu cross-compile substitution), and a
   "Validation, once an Image exists" section with the exact S2/S3 harness
   commands to run once CI produces an artifact -- explicitly NOT run yet,
   no fabricated numbers.

## Note: build.sh was iterated between the two dockerd-crash attempts
When I came back to write the CI docs, `build.sh` on disk no longer matched
what I'd originally written -- it now extracts the kernel source into a
named docker volume (`rask-kernel-src`) instead of a bind-mounted
`work/src/linux-<version>/` directory, and downloads resumably (`curl -C -`
against a `.partial` file). This was an intermediate fix attempt (virtiofs
bind mounts are slow for a tree this size and mangle tar's permission/
ownership bits) made between the two crash attempts the coordinator
referenced -- not something I wrote in this pass. I did not revert it (it's
a real improvement); I only:
- Added a hard `exit 1` in the in-container mismatch check so a config
  regression fails the build loudly instead of just printing `MISMATCH:`
  and continuing into a doomed compile (matters more now that CI is the
  only place this runs).
- Removed an unused `SRC_DIR` variable left over from the bind-mount
  version.
- Updated README's "Pipeline" section to describe the volume-based
  extraction accurately instead of the stale bind-mount description.
`.github/workflows/kernel.yaml`'s cache step only targets
`tools/kernel/work/src` (the downloaded tarball) and `work/ccache/` -- the
`rask-kernel-src` docker volume itself is not cacheable via
`actions/cache` and doesn't need to be: each CI run is a fresh runner, and
re-extracting from a cached tarball is a few seconds, not a bottleneck.

## Final state (end of this session)
- No Image was produced; no boot/S2/S3 validation was performed (honest in
  README/PROGRESS, not fabricated).
- rask.config is verified internally consistent (zero Kconfig mismatches)
  via a config-only dry run, but the actual compile has never completed --
  flagged as a known gap in README ("a clean config doesn't guarantee a
  clean build").
- Local docker/colima state confirmed clean: no rask-related containers or
  images remain; colima and the user's other containers untouched.
- tools/kernel/work/ still holds the cached linux-6.6.142.tar.xz + extracted
  source tree (useful cache for whoever runs this next, gitignored, left in
  place -- not cleaned up since nothing asked for that and it's harmless
  disk usage).
- Next actual owner action: trigger `.github/workflows/kernel.yaml` (push a
  tools/kernel/ change or workflow_dispatch), then run the "Validation,
  once an Image exists" steps in README.md against the resulting artifact.
