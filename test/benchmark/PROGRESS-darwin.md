# M1 macOS (vz) productionization: progress tracker

Not committed; scratch state for this session so context-compaction / a
connection drop can resume without re-deriving status. Mirrors
test/benchmark/PROGRESS.md's format for the Linux hostproc substrate.

## Done

- [x] Read plan-m0-spikes.md, research-m0-spikes.md, spikes/{s2,s3,s4,s5}/RESULTS.md,
      test/benchmark/PROGRESS.md (Linux learnings + pkill incident postmortem), and
      existing internal/{bootstrap,pki,cluster,components,manifests,substrate,store}
      + cmd/rask.
- [x] internal/components additions, all verified against the real network:
      - kernel.go: EnsureGuestKernel — downloads Alpine linux-virt 6.12.98-r0 aarch64,
        extracts the EFI-ZBOOT vmlinuz-virt payload (zimg header parse, verified byte
        offsets against a real downloaded file) into a raw arm64 Image vz can boot.
      - iptables.go: EnsureIPTablesBundle — musl+libmnl+libnftnl+libxtables+iptables
        (Alpine v3.21 aarch64, full dependency closure resolved from APKINDEX),
        preserving symlinks (new extractTarGzPreserveSymlinks), for kube-proxy's
        iptables mode (execs iptables-save/-restore, not otherwise available in an
        all-static-Go initramfs).
      - e2fsprogs.go: EnsureE2fsprogsBundle — mkfs.ext4 + its 6-package dependency
        closure, for formatting the per-cluster data disk from inside the guest.
      - cabundle.go: EnsureCABundle — curl.se's Mozilla CA bundle mirror, for guest
        containerd registry pulls.
      - cache.go: added Dir() accessor.
      All real-network-verified (RASK_VERIFY_NETWORK=1 gated tests + one full
      buildTemplateInitramfs run: 740MB template, ~48s, all real downloads).
- [x] internal/guestinit (new, portable, no build tags): moduledep.go (ParseModulesDep +
      ResolveLoadOrder, topological — verified against the REAL pinned kernel's
      modules.dep), bootparam.go (rask.cluster=/rask.boottime= kernel cmdline params),
      binfmt.go (ported from spikes/s3, Rosetta binfmt_misc registration),
      wantedmodules.go (the shared host/guest module list — verified it resolves
      cleanly against the real kernel).
- [x] internal/guestagent (new, portable): HTTP wire protocol shared by rask-init's
      guest control agent and the host's client (kubeconfig/timeline fetch,
      exec with HTTP-trailer exit code, file write).
- [x] internal/guestlayout (new): fixed guest-side paths (BinDir, ModulesDir,
      DataDiskDevice=/dev/vda since root is tmpfs not a disk, etc.) shared between the
      host-side initramfs builder and rask-init, so a path typo is a compile error.
- [x] cmd/rask-init (new, linux/arm64 only): full guest PID 1 — mountBase, module
      loading (guestinit-driven), tmpfs switch_root (mount tmpfs, copy tree, MS_MOVE +
      chroot — NOT util-linux's discard-and-exec variant, since a Go binary doesn't
      need to re-exec itself after chroot), mkfs.ext4 + mount the data disk, static
      network config (192.168.127.2/24, ported from spikes/s4), Rosetta mount +
      binfmt registration (ported from spikes/s3, now via internal/guestinit), runs
      internal/bootstrap.Boot unchanged (same DAG as internal/substrate/hostproc) +
      internal/manifests (CoreDNS/local-path-provisioner) inline, then serves the
      guestagent HTTP control API. Cross-compiles clean (GOOS=linux GOARCH=arm64,
      CGO_ENABLED=0), golangci-lint 0 issues.
- [x] internal/substrate/vz/embedded: cmd/rask-init cross-compiled and embedded via
      go:embed into the rask binary (placeholder committed, `make build-rask-init`
      regenerates the real ~40MB stripped binary) — a released rask binary needs no
      source tree at runtime.
- [x] internal/substrate/vz: cpio.go (hand-rolled newc cpio writer — no stdlib
      support — cross-validated against the system `cpio` binary), initramfs.go
      (buildTemplateInitramfs: real end-to-end run produced a 740MB template in ~48s),
      network.go (gvisor-tap-vsock wiring per spikes/s4's exact recipe, extended with
      a second Forwards entry for the guest agent), agentclient.go (host-side HTTP
      client, unit tested against httptest), vm.go (vz.VirtualMachineConfiguration
      wiring: boot loader, console, entropy, network, virtio-blk disk, Rosetta gated
      on host availability, persisted machine identifier), disk.go, state.go
      (cross-process vm-host PID + forwarded-port handoff, mirroring hostproc's
      PID-persistence pattern), vmhost.go (the "rask __vm-host" detached child
      process body), vz.go (substrate.Runtime: Create/Start/Stop/Delete/Exec/
      WriteFile; PortForward deliberately not implemented, documented why — no
      caller needs it in v1).
- [x] cmd/rask: __vm-host hidden subcommand (darwin-only; linux stub returns nil),
      substrate_darwin.go updated to pass homeDir into vz.New.
- [x] Makefile: build-rask-init + codesign targets; vz.entitlements committed.
      `make build && make codesign` verified working end to end
      (`codesign -d --entitlements` shows com.apple.security.virtualization).
- [x] Repo-wide: go build/vet clean on darwin AND linux/arm64 cross-compile,
      golangci-lint 0 issues, go test -race -shuffle=on -count=1 clean across
      every package (including the new ones).

## Design deviations from plan-m0-spikes.md (documented, not silent)

- **No per-cluster cpio.** The plan anticipated a second, per-cluster cpio archive
  (PKI, kubeconfigs, rendered configs) concatenated onto the template. v1 instead
  passes only ClusterName + BootTimeUnixNano via kernel cmdline
  (guestinit.BootParams) and lets rask-init call internal/bootstrap.Boot exactly
  like hostproc does, generating PKI/configs fresh in-guest at boot. Simpler, reuses
  hostproc's proven code unmodified, and loses nothing v1 needs (the anticipated
  benefit — pre-generated PKI for snapshot/restore stability — is v2 scope).
- **DataDiskDevice is /dev/vda, not /dev/vdb.** Root is the initramfs/tmpfs (no
  virtio-blk root device at all), so the data disk is the guest's *only* virtio-blk
  device.
- **iptables/e2fsprogs bundled as dynamic Alpine binaries + musl**, not built
  statically. kube-proxy's iptables mode execs real iptables-save/-restore
  binaries, which don't exist as a practical pure-Go/static alternative; bundling
  Alpine's own dynamically-linked build (+ musl loader + its 5 shared-lib deps) is
  the pragmatic choice given session time constraints, documented as a real
  dependency chain (verified against APKINDEX) rather than guessed.

## First real E2E boot attempts (Apple Silicon, live)

- [x] **Attempt 1 (e2e1)**: VM booted, kernel loaded, rask-init ran, mountBase/
      clock/module-loading started. FATAL: `kernel/lib/libcrc32c.ko.gz: init_module:
      no such file or directory` aborted the ENTIRE boot (loadModules returned an
      error on the first per-module failure). Root cause (confirmed via
      `crc32c_generic.ko` investigation below) plus an architectural fix both
      applied: loadModules() no longer aborts on one module's init_module failure
      — logs `RASK-INIT-MODULE-FAILED` and continues (matches plan-m0-spikes.md's
      "kube-proxy can also run in a degraded mode" guidance). Bumped
      templateInitramfsVersion v1->v2.
- [x] **Attempt 2 (e2e2)**: Got much further with the resilience fix. New real
      findings:
      1. nf_nat/iptable_nat/xt_nat/xt_conntrack/xt_MASQUERADE/xt_CT all failed
         with "Unknown symbol nf_ct_*" (nf_conntrack's exported symbols missing).
         Root cause: nf_conntrack's (and libcrc32c's) own init() calls
         crypto_alloc_shash("crc32c", ...), which requires a REGISTERED crc32c
         crypto-API transform — provided by a SEPARATE module,
         kernel/crypto/crc32c_generic.ko.gz, that modules.dep does NOT list as a
         dependency of anything (resolved by algorithm name at runtime, not
         symbol linkage, so depmod can't see it). Fix: added "crc32c_generic" as
         the FIRST entry in guestinit.WantedModules (internal/guestinit/wantedmodules.go).
      2. `mkfs.ext4 /dev/vdb: exit status 1 (file does not exist)` — found a real
         bug: I had documented the /dev/vda (not /dev/vdb) deviation in this file
         but never actually changed the guestlayout.DataDiskDevice constant. Fixed
         (internal/guestlayout/guestlayout.go). Kernel's own boot log confirms the
         disk is really named vda: `virtio_blk virtio2: [vda] ... 30.0 GiB`.
      Bumped templateInitramfsVersion v2->v3.
- [x] **Attempt 3 (e2e3)**: Both fixes above worked — modules loaded clean (no
      MODULE-FAILED lines at all), data disk formatted+mounted
      (`EXT4-fs (vda): mounted filesystem ... r/w`), network up, Rosetta mounted.
      New failure, much later: `bootstrap: launching process "containerd":
      fork/exec /opt/rask/bin/containerd: no such file or directory` even though
      the file demonstrably exists in the cpio archive at the right path with the
      right bytes (verified independently via the system `cpio` binary).
      **Root cause found via `file`**: containerd, ctr and kubelet (unlike
      kube-apiserver/-controller-manager/-scheduler/-proxy, kubectl, kine,
      containerd-shim-runc-v2, runc, CNI plugins — all static) are dynamically
      linked against **glibc** (`interpreter /lib/ld-linux-aarch64.so.1`), which
      doesn't exist anywhere in an initramfs built entirely around musl (iptables/
      e2fsprogs bundles) and static Go binaries. Exec fails with a *misleading*
      "no such file or directory" — the real cause is the missing ELF
      interpreter/loader, not the target binary itself.
      **Fix**: internal/components/gcompat.go (EnsureGCompatBundle) — Alpine's own
      supported answer to "run glibc binaries on musl": gcompat + libucontext +
      musl-obstack (+ already-cached musl), providing /lib/ld-linux-aarch64.so.1
      and libc.so.6/libpthread.so.0/libresolv.so.2 (confirmed via `strings
      <binary> | grep '\.so'` against containerd/ctr/kubelet's actual NEEDED
      entries) as symlinks to one shim library (libgcompat.so.0). Wired into
      initramfs.go alongside the iptables/e2fsprogs bundles. Bumping
      templateInitramfsVersion v3->v4, retrying next.

- [x] **Attempt 4 (e2e4, templateInitramfsVersion v4)**: the gcompat fix worked —
      containerd started. Hit a cpio symlink-safety bug immediately on the *next*
      create attempt before a VM even booted: EnsureGCompatBundle's package
      (gcompat's own apk) ships `lib64/ld-linux-aarch64.so.1 -> ../lib/ld-linux-aarch64.so.1`,
      a "../"-relative symlink that legitimately stays inside destDir but was
      rejected by extractSymlink's original "no slashes allowed" rule (meant to
      block traversal, but too strict — it couldn't tell "contained" `../` from
      "escaping" `../../../etc/passwd`). Fixed `internal/components/iptables.go`'s
      `extractSymlink` to resolve the symlink target relative to destDir and check
      containment via `filepath.Rel`, not just reject any `/`. Added
      `TestExtractTarGzPreserveSymlinks_AllowsContainedParentDirTarget` (real
      gcompat-shaped fixture) alongside the existing escape-rejection test.
- [x] **Attempt 5 (e2e5, templateInitramfsVersion v5)**: booted clean through
      module load / disk mount / network / Rosetta-was-still-enabled-at-this-point,
      then **hung with zero console output for 3+ minutes** inside
      `internal/bootstrap.Boot` (no RASK-INIT-BOOT-FAILED, no RASK-INIT-FATAL — the
      guest agent's :7777 port refused every connection the whole time, meaning
      `runBoot` was still blocked). Root cause: `cmd/rask-init/boot.go`'s `runBoot`
      called `bootstrap.Boot(ctx, ...)` with `context.Background()` (no deadline) —
      if any single component's readiness poll never succeeds, PID 1 blocks
      forever with no way to observe why (no shell in this guest to inspect
      per-component log files on the mounted disk). **Fixes applied** (both real,
      not just diagnostic): (1) `runBoot` now wraps its ctx with a 3-minute
      `bootTimeout`, so a stuck boot always terminates with a real error instead of
      hanging PID 1 indefinitely; (2) added `dumpComponentLogs` — on any boot
      failure, tails every `dataDir/logs/*.log` file to the console (the only
      channel the host can observe), since those logs are otherwise stranded on a
      disk the host can't read while the guest holds it.
      Killed this attempt (SIGTERM to vm-host) once these fixes were ready rather
      than waiting out the un-bounded original hang. Teardown was clean: no
      orphaned VM this time (see the safety incident below for a case where it
      wasn't).
- [x] **Attempt 6 (e2e6, templateInitramfsVersion v5)**: with the bounded timeout +
      log dump, **got the furthest yet**: containerd, kine, kube-apiserver,
      kube-controller-manager, kube-scheduler, kubelet all started; kubelet
      successfully registered the node (`"Successfully registered node"
      node="rask-node"`). Only kube-proxy failed, with two distinct, now-diagnosed
      causes (its own dumped log):
      1. `could not find nftables binary: exec: "nft": executable file not found`
         — expected/harmless: kube-proxy's cleanup path probes for nft-mode
         leftovers on every start; we only bundle iptables-legacy-compatible
         binaries (internal/components/iptables.go's xtables-nft-multi bundle),
         not the separate `nft` CLI. Not the actual failure.
      2. `"Error running ProxyServer" err="iptables is not available on this host"`
         — the real failure. Traced into the actual upstream source
         (kubernetes/kubernetes v1.33.13, `cmd/kube-proxy/app/server_linux.go`'s
         `platformCheckSupported` → `k8s.io/kubernetes/pkg/util/iptables.NewDualStack`
         → `runner.Present()`, which calls `ChainExists(TableNAT, ChainPostrouting)`
         — i.e. it actually execs `iptables -C ...`/checks via the runner, not just
         a `--version` probe). **Not yet root-caused to a specific fix** — plausible
         candidates identified but unverified: (a) the bundled `/usr/sbin/iptables`
         symlink to `xtables-nft-multi` needs argv[0] to literally be "iptables" for
         the busybox-style dispatch to work (should be fine via `exec.Command`'s
         normal argv[0] handling, but unconfirmed against this exact binary); (b)
         `/run/xtables.lock` (the lock file iptables-nft needs to create) — `/run` is
         created by `mountBase()` as part of the initramfs but never explicitly
         `MkdirAll`'d after `switchRoot`'s tmpfs move, worth checking; (c) the
         `ip_tables`/`iptable_nat`/`x_tables` kernel modules loaded fine per the
         console log, so it's unlikely to be a missing-module issue this time. This
         is the actual next debugging step for whoever picks this up — the
         real k8s source, once fetched, made the failure mode much clearer than
         guessing would have (see the raw fetched files' grep output preserved in
         this session's tool history if needed).

## Policy changes during this session (user-directed, both still in effect)

- **Rosetta disabled entirely** (2026-07-30, after this session's E2E runs
  correlated in time with real, repeated system-wide host crashes — Chrome and
  other processes affected, not just rask). Per explicit user direction:
  - `internal/substrate/vz/vm.go`: `attachRosetta` is no longer called from
    `buildVirtualMachineConfiguration` at all — not even
    `LinuxRosettaDirectoryShareAvailability()` is invoked. The function itself is
    kept (not deleted) for a future revisit, referenced only via a
    `disabledFeatures` struct literal (keeps it compiling/non-dead without
    exposing any enable flag — explicitly no flag was to be added).
  - `cmd/rask-init/main.go`: `mountRosettaAndRegisterBinfmt` is no longer called
    from `main`. Same `disabledFeatures`-reference pattern in `rosetta.go`.
  - Practical effect: only arm64 guest images work until this is revisited;
    amd64-via-Rosetta (plan-m0-spikes.md item 5) is out of scope for now.
  - **Not yet confirmed whether Rosetta was actually the cause** — the crash
    recurred even after this change was directed (see below), so it may have
    been a contributing factor, unrelated, or one of several. Left disabled
    regardless per explicit instruction; do not silently re-enable without
    asking.
- **Full VM-execution freeze** (2026-07-30, after a second, more severe
  recurrence of the host crash during this session — again affecting unrelated
  host processes, not just rask). Per explicit user direction: **no `rask
  create`, no spike harness execution, no VM boot of any kind on this Mac for
  the remainder of this session.** All remaining work in this session is
  code-only, verified via `go build`/`go vet`/`golangci-lint`/`go test` (which
  do not start a VM), never via actually running `rask create`.

## Safety incident found + fixed during this session (independent of the crash)

While investigating the E2E hang, a **real, latent broadcast-signal
vulnerability** was found and fixed before it could be triggered:

- Three leftover cluster directories (`e2e3`, `e2e5`, `e2e6`) were found with
  `vm-host.pid` containing the literal text `-1` (written externally — the
  Runtime.Start() code path always writes a real `cmd.Process.Pid`, which
  Go's os/exec guarantees is positive, so this could not have come from a
  successful Start()).
- `internal/substrate/vz/vz.go`'s `readPID` previously only checked that the
  pidfile parsed as *some* integer, not that it was positive. Had `Stop()` or
  `Delete()` been run against one of these directories as-is, it would have
  called `syscall.Kill(-1, syscall.SIGTERM)` — POSIX `kill(2)` treats pid `-1`
  as "every process the caller may signal" (pid `0` similarly means "every
  process in the caller's process group"), which would have broadcast SIGTERM
  far beyond this one cluster's vm-host process, to every signalable process
  on the host.
- **Fixed**: `readPID` now rejects `pid <= 0`, treating it the same as "not
  running" (matches `Stop`/`Delete`'s existing idempotent-no-op contract for a
  missing pidfile). Regression test:
  `TestRuntime_Delete_TreatsMalformedPIDAsNotRunning` (table-driven: `-1`, `0`,
  `-42`, empty, non-numeric — all five must be treated as "not running", not
  cause a signal attempt).
- The three leftover directories were **not** cleaned up via `rask delete`
  (the VM-execution freeze arrived before that could happen safely-in-hindsight
  — though with the fix applied, `rask delete` against them would now be safe).
  They remain under `~/.rask/clusters/{e2e3,e2e5,e2e6}` for manual cleanup
  (`rm -rf`) at the user's discretion; no live process is associated with any
  of them (verified: no matching PIDs, and the only running
  `Virtualization.framework` VM process on this host throughout this session's
  cleanup work was independently confirmed via `lsof` to belong to the user's
  own `colima` VM, not rask — never touched).

## Additional hardening implemented per explicit user direction (code-only, unverified by execution)

Implemented after the VM-execution freeze; **none of this has been exercised
against a real VM** — only `go build`/`go vet`/`golangci-lint run`/`go test
-race` (all clean, repo-wide, both darwin and linux/arm64 cross-compile).

- [x] **`vm.go`: `panic=-1` → `panic=0`** on the kernel command line (already
      applied by the user directly before this list was written; confirmed
      present). A panicking guest now halts instead of reboot-looping —
      `panic=-1` (instant reboot) turned every failed boot into an infinite
      CPU-burning loop, and a leftover VM from a failed create could keep
      rebooting forever with nothing left to stop it.
- [x] **Guaranteed graceful teardown of vm-host + VM on create failure.**
      New `internal/substrate/vz/terminate.go`: `terminateVMHost(ctx, pid,
      gracePeriod)` — SIGTERM first (so `RunVMHost`'s own signal handling,
      via `context` cancellation, runs its deferred `stopVM`/`console.Close`/
      `net.Close` and actually stops the underlying VM), SIGKILL only as a
      timeout/ctx-cancellation escalation. Previously, `Start`'s failure-cleanup
      path (and, less critically, one branch of `Stop`) went straight to
      SIGKILL — which can't be caught, so `RunVMHost`'s cleanup defers never
      ran, orphaning a live Virtualization.framework VM process with nothing
      left to stop it. This is *exactly* the class of bug that produced the
      orphaned VM found and misattributed-then-correctly-attributed during this
      session's own investigation (turned out to be the user's `colima` VM, not
      rask's — but the mechanism by which a rask VM *could* end up in that state
      was real and is now fixed). `Start`'s failure defer also now captures
      `pid` directly from the in-memory return value rather than re-reading the
      pidfile, closing a gap where a pidfile-write failure would have left
      nothing to clean up. Unit tested against a real (harmless) `sleep`
      process standing in for vm-host — `TestTerminateVMHost_*` (SIGTERM
      success, no-op-when-already-dead, ctx-cancellation escalates to SIGKILL
      quickly) — including a fix for the exact "unreaped zombie still looks
      alive to kill(pid,0)" test-harness gotcha already documented in
      test/benchmark/PROGRESS.md's own hostproc tests.
- [x] **Host-wide single-VM lock.** New `internal/substrate/vz/lock.go`:
      `acquireVMLock(homeDir)` takes an exclusive, non-blocking `flock(2)` on
      `~/.rask/vm.lock`, held by the `vm-host` process for its entire lifetime
      (released automatically by the kernel on any process exit, including
      SIGKILL — never stuck-held by a crash). `RunVMHost` acquires it as the
      very first thing, before any kernel/template/disk work, so a second
      concurrent `rask create` fails fast with a clear `ErrAnotherVMRunning`
      instead of starting a second VM. Deliberately global, not per-cluster,
      until there's evidence concurrent VMs are safe on this substrate — this
      whole hardening pass exists because they might not be. Unit tested
      (`TestAcquireVMLock_*`: second-acquire fails, released lock is
      reacquirable, creates homeDir if missing).
- [x] **Boot deadline watchdog in vm-host.** New
      `internal/substrate/vz/watchdog.go`: `runBootWatchdog` runs as its own
      goroutine once the VM has started, polling the guest's own control agent
      (through the same forwarded port `RunVMHost` already set up) for up to
      `bootWatchdogTimeout` (6 minutes); if the guest never reports healthy —
      and the reason isn't just an ordinary Stop happening concurrently, which
      the implementation distinguishes via `ctx.Err()` — it stops the VM and
      lets `RunVMHost` exit on its own. This makes `vm-host` self-bounding:
      previously its only lifetime bound was the external `rask create`
      CLI process's own `bootTimeout`, which is useless if that CLI process
      itself dies without ever sending Stop (exactly what an interactive
      session restart does — observed directly during this session).
      Unit tested against `httptest` servers (healthy immediately: no
      cancellation; never healthy: cancels + reports failure within the given
      timeout; ctx already canceled externally: does NOT misreport as a boot
      timeout).
- [x] **Explicit VM memory ceiling + pre-boot free-memory check.** The VM's
      memory ceiling was already an explicit, named constant
      (`defaultMemoryMiB = 4096`, enforced by
      `vz.NewVirtualMachineConfiguration` as a hard guest RAM cap). Added: new
      `internal/substrate/vz/memcheck.go`, `checkFreeMemory` — parses real
      `vm_stat` output (page size read from its own header, not assumed: it's
      16384 on this Apple Silicon host, not the historical 4096 default) and
      refuses to boot unless free+inactive host memory is at least 1.5x the
      VM's configured ceiling. `RunVMHost` calls this immediately after
      acquiring the VM lock, before any expensive kernel/template work. Unit
      tested against a real `vm_stat` output sample captured on this host
      (`TestParseVMStatFreeMiB_RealSample`) plus synthetic threshold cases.

## Honest final status

**E2E is UNVERIFIED as of the VM-execution freeze.** The furthest confirmed
real boot (attempt 6, before the freeze) reached: kernel boot → module load →
tmpfs switch_root → data disk format/mount → network → containerd/kine/
apiserver/controller-manager/scheduler/kubelet all started → node registered
→ **kube-proxy failing on iptables detection** (root-caused to
`k8s.io/kubernetes/pkg/util/iptables`'s `Present()` check, exact fix not yet
identified — see attempt 6 above for the leading candidates). CoreDNS,
local-path-provisioner, and a smoke pod have never been reached. No p50/p95
numbers exist; RESULTS-darwin.md was never created because nothing ever
completed a full create→Ready cycle.

Everything landed in this session past that point (the safety fixes, the
Rosetta disable, the lock/watchdog/memcheck hardening) is **implemented,
unit-tested where the logic is extractable, and verified via
build/vet/lint/test only** — none of it has been exercised against a real VM,
by explicit instruction. The next session should, once VM execution is
approved again: (1) resolve the kube-proxy iptables issue (attempt 6's leading
candidates are a good starting point), (2) get CoreDNS + local-path-provisioner
+ a smoke pod Running, (3) run the actual 10-run p50/p95 benchmark and write
RESULTS-darwin.md, (4) decide whether to re-investigate and re-enable Rosetta
now that it's known the host crash recurred even with it disabled (i.e. it may
not have been the cause, or may have been only one of several factors) — that
determination was never reached.
