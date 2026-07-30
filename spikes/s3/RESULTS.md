# Spike S3: amd64 images on Apple Silicon via Rosetta — RESULTS

**Verdict: PASS.** An arm64 vz guest pulled a real amd64-only image
(`docker.io/library/busybox`, `--platform linux/amd64`) and ran it under
Rosetta; `uname -m` inside the container reported `x86_64` in 3/3 runs.

## Numbers (3 runs, M-series Apple Silicon, macOS 26.5)

| phase | run1 | run2 | run3 |
|---|---|---|---|
| mounts + modules | ~0.3ms (+~12ms modules) | " | " |
| net up (DHCP, vz NAT) | 595ms | 97ms | 83ms |
| containerd ready | 41ms | 41ms | 41ms |
| image pull (network-bound) | 11.3s | 6.0s | 7.3s |
| container run (`uname -m`) | 88ms | 93ms | 67ms |

Pull time is registry-network-bound and irrelevant to rask's create path
(images come from the shared host cache in production). The relevant
figures: containerd ready in **41ms**, amd64 container start via Rosetta in
**~70-90ms**.

## Production recipe (for internal/substrate/vz)

1. **Kernel**: puipui (S2) lacks BINFMT_MISC/CGROUPS/OVERLAY/namespaces —
   too minimal for a k8s node. Spike used the Alpine `linux-virt` 6.6 kernel:
   - `vmlinuz-virt` is an **EFI ZBOOT** PE; vz's `VZLinuxBootLoader` cannot
     boot it directly. Extract the payload: `zimg` header at 0x04, payload
     offset/size (LE32) at 0x08/0x0c, payload is gzip → raw arm64 `Image`.
   - Modules loaded by init (dependency order, `unix.InitModule` on
     gunzipped .ko.gz, ~12ms total): rng-core, virtio-rng, af_packet,
     failover, net_failover, virtio_net, fuse, virtiofs, binfmt_misc,
     overlay.
   - **rask's own kernel should build these in** (=y) to skip module
     handling entirely; Alpine kernel is a spike vehicle.
2. **Entropy**: host must attach `VZVirtioEntropyDeviceConfiguration` AND
   the guest needs the virtio-rng driver, else getrandom(2) blocks (DHCP
   client hung 2min on "could not get random number").
3. **Rosetta**: host `VZLinuxRosettaDirectoryShare` (tag "rosetta") →
   guest `mount -t virtiofs rosetta /mnt/rosetta` → write registration to
   `/proc/sys/fs/binfmt_misc/register`:
   - magic/mask: ELF64 LE EM_X86_64, EI_OSABI masked out, e_type LSB masked
     (ET_EXEC|ET_DYN) — see init/binfmt.
   - flags `OF` (open-binary + fix-binary) so the interpreter works across
     runc's mount namespaces / pivot_root.
   - **Do NOT append a trailing NUL to the registration string** — the write
     fails with EINVAL (verified on 6.6 and 6.8; a bare unterminated line
     works on both).
4. **containerd**: 2.x config, accepts amd64 manifests once the pull/run is
   done with explicit `--platform linux/amd64` (production: set CRI
   platforms / use platform-specific pull).
5. **pivot_root**: containers cannot pivot_root when the system root is the
   initramfs (ramfs) — spike used `ctr run --no-pivot`. Production rask
   boots from a virtio-blk root filesystem, where normal pivot_root works;
   no flag needed.
6. **TLS**: guest needs a CA bundle at /etc/ssl/certs/ca-certificates.crt
   for registry pulls (spike copied the macOS system bundle).

## Follow-ups for production
- Build rask's own guest kernel (Kata-config-derived) with the module list
  above compiled in.
- Pull via the daemonless shared host image cache (S4's gvisor-tap-vsock
  registry-mirror pattern) instead of in-guest registry pulls.
- Measure Rosetta overhead vs native arm64 on a realistic workload (haro's
  images) in M3.
