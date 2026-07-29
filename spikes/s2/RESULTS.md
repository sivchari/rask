# Spike S2: vz boot-to-init latency (macOS)

## Question

Does a minimal Linux VM on Apple's Virtualization.framework (via
`github.com/Code-Hex/vz/v3`) boot to a Go PID 1 (`rask-init`) in under
2 seconds?

## Result

**Yes, by roughly 15x margin.** Boot-to-init-marker (`t2`) p50 = 125ms,
p95 = 135ms across 10 runs, 1 vCPU / 1GiB. This matches the research
note's sub-200ms expectation (libkrun/krunkit-class numbers), not just
the <2s target.

## Environment

- Host: Apple Silicon (arm64), macOS 26.5.2 (build 25F84), Darwin 25.5.0
- Go: `go1.27-devel` (`/Users/takuma.shibuya.001/workspace/go/go/bin/go`), `darwin/arm64`
- vz binding: `github.com/Code-Hex/vz/v3` v3.7.1
- Spike module: `spikes/s2/go.mod` (`module rask-spike-s2`), independent of
  the repo root `go.mod` to avoid conflicting with parallel work there.

## What was built

- `spikes/s2/init/main.go` — `rask-init`: static
  `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` binary, no libc, ~1.7MB
  unstripped / smaller with `-s -w`. As PID 1 it mounts `/proc`, `/sys`,
  `/dev` (devtmpfs), prints `RASK-INIT-BOOT-COMPLETE t=<RFC3339Nano>
  unixnano=<n>` to stdout (the virtio console), then blocks on
  `SIGCHLD` and reaps with a non-blocking `wait4(-1, WNOHANG)` loop. No
  shell, no busybox.
- `spikes/s2/main.go` — host measurement harness using `vz`. Builds a
  `VZLinuxBootLoader`-equivalent config (`vz.NewLinuxBootLoader` +
  `WithCommandLine("console=hvc0 reboot=t panic=-1")` +
  `WithInitrd(...)`), a `VirtioConsoleDeviceSerialPortConfiguration`
  backed by an `os.Pipe()` so the host can read guest console output, and
  a `VirtioEntropyDeviceConfiguration` (recommended by vz's own test
  setup to avoid RNG-starved boot stalls). No disk device for v0 — this
  measures initramfs-only boot as specced; disk attach was not needed to
  answer the boot-time question and was left out of scope.
- `spikes/s2/fetch.sh` — downloads the kernel (see below).
- `spikes/s2/build-initramfs.sh` — cross-builds `rask-init` and packs a
  newc cpio archive containing only `/init`.
- `spikes/s2/vz.entitlements` + ad-hoc codesign
  (`codesign --entitlements vz.entitlements -s - ./work/spike-s2`) for the
  `com.apple.security.virtualization` entitlement.

## Kernel choice

Used **puipui-linux v1.0.3** (`Code-Hex/puipui-linux`,
`puipui_linux_v1.0.3_aarch64.tar.gz`, ~5.3MB tarball, kernel extracts to
`work/Image`, 6.8MB uncompressed arm64 `Image`). This is the exact
kernel the `Code-Hex/vz` binding's own CI test suite (`Makefile`
target `download_kernel`, `virtualization_test.go`) boots for its
integration tests against `VZLinuxBootLoader` — it has virtio-console,
virtio-net, and virtio-vsock built in, is already uncompressed
(arm64 `VZLinuxBootLoader` requires this — `Image.gz` must be
`gunzip`ed first), and is version-matched to the vz release used here.

This was chosen over the three candidates named in the spike brief:

- **Kata Containers guest kernel**: purpose-built for fast microVM boot
  and worth using once rask needs a production-grade kernel config, but
  the release tarball bundles a full Kata release (agent, rootfs,
  QEMU/Cloud-Hypervisor binaries — hundreds of MB) with the kernel
  nested several directories deep, and its default virtio wiring targets
  Kata's own agent/vsock protocol rather than a bare
  `vz.FileHandleSerialPortAttachment` console. Overkill to verify for a
  single boot-latency measurement.
- **Firecracker CI kernels**: tuned for Firecracker's minimal MMIO
  device model, not vz's virtio-console/virtio-net; would need
  independent verification that vz's device set is even compatible.
- **Alpine `vmlinuz-virt`**: gzip-compressed (`vz.NewLinuxBootLoader`
  rejects this on arm64 — confirmed by reading `bootloader.go`, which
  just does `os.Stat` and hands the path straight to the Objective-C
  loader, i.e. no decompression happens on the vz side) and virtio is
  commonly built as loadable modules rather than statically enabled,
  which would need bundling `.ko` files into the initramfs.

`fetch.sh` documents this reasoning inline and is idempotent (skips the
download if `work/Image` already exists).

## Measurement methodology

- `t0` = `time.Now()` immediately before `vm.Start()`.
- `t1` = time of the first byte read from the console pipe (kernel's own
  boot log reaching virtio-console).
- `t2` = time the console reader sees the `RASK-INIT-BOOT-COMPLETE` line
  from `rask-init`.
- Both `t1` and `t2` are reported as durations since `t0`.
- Each run creates a brand-new `vz.VirtualMachine` (cold `VirtualMachine`
  object per run, as permitted by the task); the VM is force-stopped
  (`vm.Stop()` / `RequestStop()` fallback) before the next iteration.
- Command: `./work/spike-s2 --runs 10 --timeout 10s`

## Results (10 runs each)

### 1 vCPU / 1GiB (default)

```
run 01: t1=131.83ms t2=133.40ms
run 02: t1=130.28ms t2=131.93ms
run 03: t1=121.25ms t2=123.07ms
run 04: t1=123.91ms t2=128.63ms
run 05: t1=128.56ms t2=130.11ms
run 06: t1=121.32ms t2=123.13ms
run 07: t1=130.19ms t2=131.75ms
run 08: t1=121.71ms t2=123.44ms
run 09: t1=118.13ms t2=119.65ms
run 10: t1=124.02ms t2=125.50ms

t1: p50=123.91ms p95=131.83ms min=118.13ms max=131.83ms
t2: p50=125.50ms p95=133.40ms min=119.65ms max=133.40ms
```

### 2 vCPU / 2GiB

```
run 01: t1=131.21ms t2=133.02ms
run 02: t1=123.63ms t2=125.55ms
run 03: t1=119.00ms t2=120.74ms
run 04: t1=122.17ms t2=125.72ms
run 05: t1=117.66ms t2=119.63ms
run 06: t1=125.90ms t2=128.16ms
run 07: t1=132.47ms t2=134.98ms
run 08: t1=126.05ms t2=128.04ms
run 09: t1=119.90ms t2=121.66ms
run 10: t1=127.60ms t2=129.52ms

t1: p50=123.63ms p95=132.47ms min=117.66ms max=132.47ms
t2: p50=125.72ms p95=134.98ms min=119.63ms max=134.98ms
```

**Verdict: t2 (`vm.Start()` -> `rask-init` marker) p50 = 125-126ms,
p95 = 133-135ms, ~15x under the 2s target, with no measurable
difference between 1 vCPU/1GiB and 2 vCPU/2GiB.** `t1` and `t2` are
within 1-3ms of each other on every run (see "vz API surprises" below
for why that's not really two independent measurements).

### First-run vs. warm-cache

The very first invocation after a fresh shell (before any of the 10-run
batches, kernel/initramfs not yet in the page cache) measured
`t2=370ms` — about 3x the warm p50, still comfortably under 2s. All
subsequent runs in the same or later processes were consistently in the
115-135ms band, confirming host-side page-cache warmth (kernel Image +
initramfs file reads) is the dominant source of the outlier, not VM
teardown/recreate overhead.

## Memory footprint

Host `spike-s2` process (`/usr/bin/time -l`, 3 boot+stop cycles):
maximum resident set size 16.0MB, peak memory footprint 5.6MB. Guest
memory (1-2GiB as configured) is not reflected in host RSS — vz backs
guest RAM lazily and the guest here only touches a small fraction of it
before being stopped, so this number says nothing about steady-state
guest memory cost, only that the host-side control process itself is
cheap.

## vz API surprises

- **No entitlement friction.** The ad-hoc codesign with
  `com.apple.security.virtualization` in `vz.entitlements` was
  sufficient; no sandbox bypass was needed to run `vm.Start()` from
  this Bash tool — first attempt just worked.
- **No explicit run loop management required** in the Go bindings.
  `vz.NewVirtualMachine` creates its own internal `dispatch_queue`
  (`makeDispatchQueue` in the cgo layer); `Start()`/`Stop()` block on a
  Go channel fed by an Objective-C completion handler. Unlike some
  Cocoa/AppKit patterns there was no need for `runtime.LockOSThread()`
  or a manual `CFRunLoopRun()` — confirmed against the binding's own
  `virtualization_test.go`, which calls `vm.Start()` directly from a
  `go test` goroutine.
- **`FileHandleSerialPortAttachment` read/write naming is inverted from
  intuition.** `NewFileHandleSerialPortAttachment(read, write)`: the
  `read` file is what the *framework* reads from to send bytes *into*
  the guest (host->guest input), and the `write` file is what the
  framework writes guest output *into* (so the host must read from
  it to see console output, i.e. it's really "guest stdout"). Getting
  this backwards silently hangs the reader goroutine forever since the
  guest never sends spontaneous data on the input-direction handle.
- **arm64 requires an uncompressed kernel `Image`**, confirmed by
  reading `bootloader.go` — `NewLinuxBootLoader` does nothing but
  `os.Stat` the path and hand it to `newVZLinuxBootLoader`; there is no
  decompression step anywhere in the vz layer, so a gzipped `vmlinuz`
  fails opaquely inside the Objective-C/Virtualization.framework layer
  rather than with a clear Go-side error.
- **`t1` (first console byte) and `t2` (init marker) are not
  meaningfully separable at this timescale.** Virtio-console only
  attaches once the kernel's virtio subsystem has probed devices, which
  happens quite late in boot — by the time any byte reaches the host,
  the kernel is already within a couple of milliseconds of handing off
  to `rask-init`. `t1` should be read as "boot is basically done", not
  as an early kernel-entry checkpoint. An earlier true kernel-handoff
  timestamp isn't observable from the host through this console-only
  approach; getting one would need an `earlycon=` parameter tied to the
  specific UART vz exposes (not investigated here — out of scope for a
  substrate-viability question already answered by `t2`).

## Threat assessment for rask's macOS substrate

Nothing observed here threatens using vz as rask's macOS backend:

- Boot-to-init latency (125ms p50) leaves enormous headroom against the
  2s spike target and even against k3d's 7-16s full-cluster-ready
  numbers from the research doc — this is purely the VM-boot floor, not
  the eventual "cluster Ready" latency, but it means that whatever
  dominates rask's end-to-end create time will be the control-plane
  bootstrap problem (kubelet/apiserver/etcd convergence, per the
  kind/k3d findings in research-m0-spikes.md), not microVM boot.
- 1 vCPU/1GiB vs 2 vCPU/2GiB showed no latency difference, so sizing the
  default cluster VM won't be a boot-time trade-off.
- The one real risk surfaced is process-level, not architectural: the
  serial-port read/write parameter inversion is an easy footgun to hit
  again when this code is promoted from spike to production
  (`internal/substrate/vz`), so that should get a clear doc comment or
  a small wrapper with unambiguous naming (e.g. `hostReadsFrom` /
  `hostWritesTo`) when this graduates out of the spike.
- Not yet tested here (deferred to S3/S4 per `plan-m0-spikes.md`):
  virtio-blk root disk attach, containerd on the amd64/Rosetta path, and
  gvisor-tap-vsock networking. None of those are blocked by anything
  found in this spike.
