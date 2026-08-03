# vm-host silent death — investigation and fix

Branch: `fix/vm-host-death`, worktree `.claude/worktrees/vm-host-death`, based on `origin/main` @ `57598f0`.

## Symptom

`rask __vm-host` died ~100s into a healthy cluster's life ("dev"), unrequested. Reproduced a
second time on a cluster named "demo". `kubectl` started failing with `connection refused`.
Both deaths correlated with a background `kubectl port-forward` being killed by an external
harness timeout. `~/.rask/clusters` was later found completely empty (no "dev", no "demo").

## Evidence chain

1. **`internal/substrate/vz/vz.go`'s `spawnVMHost`** used `syscall.SysProcAttr{Setpgid: true}`
   only. `Setpgid` detaches the child into a *new process group* but leaves it in its parent's
   *session*. Confirmed by direct experiment (see below) and by a live production run: a
   Setpgid-only child's `getsid()` equals its parent's, i.e. it remains reachable by that
   session's own signal delivery for the rest of its life, not just at spawn time.

2. **`cmd/rask/vmhost_darwin.go`'s `newVMHostCommand`** only registered `SIGTERM`/`SIGINT` via
   `signal.NotifyContext`. `SIGHUP` was never caught. Go's default disposition for an unhandled
   `SIGHUP` terminates the process **immediately** — no deferred cleanup runs (`stopVM`,
   `console.Close`, `net.Close`, `lock.Release` all live in `RunVMHost`'s main goroutine and
   never execute), and nothing is logged, because the process is torn down before any Go code
   can react. This is a precise match for the observed symptom: PID gone, `vm-host.pid` stale,
   zero explanatory line in `vm-host.log`.

3. **Empirical reproduction (scratchpad, no VM involved)**: built a minimal Go binary mirroring
   `vmhost_darwin.go`'s exact `signal.NotifyContext(ctx, SIGTERM, SIGINT)` call, spawned via a
   second binary mirroring `spawnVMHost`'s exact `SysProcAttr{Setpgid: true}`. A single
   `kill -HUP <pid>` killed it instantly — one heartbeat line, then silence, no shutdown message.
   Confirmed the child's session id equalled its *parent's* (not its own), proving Setpgid alone
   does not detach the session. Switching to `Setsid: true` alone made `getsid(child) ==
   child's own pid` (a real new session) but a **direct** `kill -HUP` still killed it silently
   (Setsid stops automatic session-wide propagation, it cannot stop a signal sent straight at
   the pid). Only `Setsid` + explicit `SIGHUP` handling together produced the correct, graceful
   `CLEAN-SHUTDOWN via ctx.Done()` log line under both conditions.

4. **Boot watchdog (`watchdog.go`) — ruled out.** `runBootWatchdog` calls
   `client.WaitHealthy(watchCtx)` exactly once; the goroutine returns (without calling `cancel`)
   as soon as that call succeeds, and is never rearmed. Grepped the whole `vz` package for
   `NewTicker`/`time.Tick`: none exist outside this one-shot. It cannot fire ~100s into a
   *successful* boot. `bootWatchdogTimeout` is 6 minutes, far past the ~100s window anyway.

5. **`vm.StateChangedNotify()` / vz callbacks.** Read end to end: `RunVMHost`'s select and
   `waitForTerminalOrCancel` already turned a `Stopped`/`Error` transition into a returned error,
   and `cmd/rask/main.go` already prints any `Execute()` error to `os.Stderr` (which
   `spawnVMHost` redirects into `vm-host.log`) — so *that* path was not actually silent before
   this fix. The real gap was narrower than "no logging at all": (a) non-terminal transitions
   were never logged, and (b) most importantly, an OS-level signal kill bypasses this whole
   mechanism — vz never gets a chance to report anything because the process is gone before any
   Go code runs. Added `handleVMStateChange` (logs every transition, terminal or not, before
   deciding whether to return an error) to close gap (a); (b) is closed by fix #1/#2 instead,
   since no amount of in-process logging can survive an unhandled fatal signal.

6. **`hrtimer: interrupt took 7.8ms` (console.log's last line).** Indicates the guest's vCPU
   thread went ~7.9ms between hrtimer-relevant scheduling opportunities — consistent with host
   CPU contention stalling the vCPU thread, not a guest kernel panic (no oops, no panic banner).
   Cross-referenced against macOS diagnostics (`/Library/Logs/DiagnosticReports/com.apple.
   Virtualization.VirtualMachine_*.diag`): found a real, repeated pattern of `cpu_resource.diag`
   / crash-style reports for `com.apple.Virtualization.VirtualMachine` across 7/30, 8/1, and
   three separate times on 8/3 (11:37, 11:45, 16:15) — i.e. genuine, repeated host CPU pressure
   from Virtualization.framework VMs on this machine that afternoon.
   **Correction / negative result, reported honestly**: the 16:15 report (PID 40583) was
   positively attributed via a `runningboardd` `RBAssertionManager` snapshot to
   `.limactl-wrapped` (Lima, i.e. **colima**, confirmed independently running via `ps aux`
   showing `colima daemon`/`.limactl-wrapped hostagent` since 14:30) — **not rask**. The other
   five reports carry no requesting-process field I could positively resolve either way (no
   "rask" or "lima" string anywhere in any of the six files). So: general host CPU oversubscription
   from concurrently-running Virtualization.framework VMs (rask's own plus colima's, confirmed
   both ran that day) remains a plausible contributing factor to the guest's vCPU starvation, but
   **no diagnostic report could be positively pinned on rask's own VM specifically** — I will not
   claim more than that. Every "Action taken" field I could read said `none` (informational only,
   macOS did not itself kill anything via this mechanism in the samples inspected) — this argues
   further against "macOS's CPU monitor killed the VM" and toward the signal-based mechanism
   above.

7. **jetsam.** `log show --predicate 'eventMessage CONTAINS "jetsam"' --last 6h` and a second,
   time/process-scoped query for `rask`/`vm-host`/`Virtualization.VirtualMachine` between
   16:38–16:52 returned only generic `runningboardd` "not memory-managed" noise (about
   `osascript`, Chrome, Discord, etc.) and one large `RBAssertionManager` process-state dump —
   nothing naming rask, vm-host, or an actual kill action. No JetsamEvent files found in either
   DiagnosticReports directory for the relevant window. **Inconclusive, not "ruled out"**: this
   is a negative result from the queries I ran, not proof no jetsam kill occurred.

8. **Corrected interpretation of the `tcpproxy: ... 192.168.127.2:7777 ... connection was
   refused` lines.** `internal/guestagent/protocol.go:27` confirms `Port = 7777` — this is the
   guest control agent's own port, i.e. exactly the address both `Runtime.Start`'s
   `client.WaitHealthy(bootCtx)` **and** the boot watchdog's own independent `WaitHealthy` call
   poll every 50ms while waiting for the guest to come up. Reproduced this exact log signature
   live: a fresh, healthy test cluster's `vm-host.log` showed dozens of these lines in the same
   one-to-two-second window, purely from the two concurrent 50ms-interval polling loops hitting
   the guest agent port before its HTTP server had started listening. **This means the presence
   of these lines is not, by itself, evidence that "the guest was already gone" — it is routine,
   expected boot-time noise.** I cannot fully re-verify whether the original incident's specific
   occurrences were this same boot-time noise or a later recurrence after ~100s, because the
   original `~/.rask/clusters/dev/data/vm-host.log` no longer exists (see next section) to check
   timestamps against. I'm flagging this because it directly revises a load-bearing piece of the
   evidence as originally framed, and I'd rather report the correction than let it stand
   unchallenged.

## `~/.rask/clusters` wipe — conclusion

Confirmed via direct, read-only inspection: `~/.rask/clusters/` is empty (only `.`/`..`);
`~/.rask/vm.lock` last written at 16:46 with body `"demo"`; no rask/vm-host process currently
running.

**This analysis session never ran any `rask` CLI command against the real `~/.rask`.** Every
command I ran before this investigation was `git`/`ls`/`grep`/`find`/`go`; the one live VM I
booted for fix verification used `HOME=<scratchpad>` (see below), fully isolated from
`~/.rask`.

Traced every code path that can remove a cluster's state directory (`grep -rn RemoveAll`,
non-test, non-worktree hits only):
- `internal/substrate/vz/vz.go:208` — `Runtime.Start`'s own failure-cleanup, only for the one
  name a *currently failing* `Start` call just tried to create.
- `internal/substrate/vz/vz.go:432` — `Runtime.Delete`, only reachable when `readPID` finds no
  valid pidfile.
- `pkg/cluster/provider.go`'s `Provider.Delete` — calls `p.rt.Stop` (tolerant of an
  already-dead pid: `terminateVMHost`'s `processAlive` check short-circuits to success), then
  `p.rt.Delete`, then `os.RemoveAll(internalcluster.Dir(...))`. **One `rask delete cluster
  --name <name>` call is sufficient** per cluster to fully explain the observed wipe — no
  second `rask stop` call is needed first, correcting my own earlier assumption.
- `cluster.Dir(homeDir, name) = filepath.Join(homeDir, "clusters", name)` — checked for an
  empty-`name`/path-traversal bug that could `RemoveAll` the whole `clusters/` parent in one
  shot: found none. `name` always comes from a required `--name` cobra flag or
  `defaultClusterName`, never empty in any reachable path.
- `cmd/rask/get.go`'s `rask get clusters` — confirmed pure `List()`, no mutation.

**Conclusion**: the most likely explanation is two explicit, legitimate `rask delete cluster
--name <name>` invocations (one for "dev", one for "demo") — most plausibly part of the
reproduction/cleanup harness between attempts. I found **no rask-internal auto-delete path**
that could do this without an explicit per-cluster `Delete` call, and **no evidence** (in
`~/.zsh_history`, which had zero `rask`/`kubectl` entries — inconclusive on its own, since a
non-interactive harness need not touch that file) pointing at any other mechanism. I cannot
name the exact commands or who/what ran them; I can only confirm rask's own code requires two
deliberate, per-name `Delete` calls to produce what was observed, and that this session made
none of them.

## Fixes implemented

### 1. Session isolation (`internal/substrate/vz/vz.go`, `spawnVMHost`)

`SysProcAttr{Setpgid: true}` → `SysProcAttr{Setsid: true}`. Makes vm-host both session leader
and process-group leader of a brand new session, unreachable by whatever session spawned
`rask create` for the rest of its life (not just at spawn time, which is all Setpgid ever
covered).

### 2. Explicit SIGHUP handling (`cmd/rask/vmhost_darwin.go`)

`signal.NotifyContext(ctx, SIGTERM, SIGINT)` → `signal.NotifyContext(ctx, vmHostSignals...)`
where `vmHostSignals = []os.Signal{SIGTERM, SIGINT, SIGHUP}`. Defense in depth: Setsid stops
*automatic* session-wide propagation, but cannot stop a SIGHUP sent directly at the pid (an
operator's own `kill -HUP`, or any future direct caller). Now routes through the same graceful
shutdown as SIGTERM/SIGINT instead of Go's silent default-terminate.

`vmHostSignals` is a package-level var specifically so it's unit-testable without invoking
`RunE` (which would require booting a real VM).

### 3. Log every vz VM state transition (`internal/substrate/vz/vmhost.go`)

New `handleVMStateChange(w io.Writer, st cvz.VirtualMachineState) error`: logs a timestamped
line for **every** transition (not just the two terminal ones `RunVMHost` already treated as
fatal), then returns a non-nil error only for `Stopped`/`Error`. Used from both the initial
`select` in `RunVMHost` and the loop in `waitForTerminalOrCancel` (previously duplicated
terminal-state-check logic, now shared). `w` is `os.Stderr` at both call sites, which
`spawnVMHost` redirects to `vm-host.log`.

## Tests added

- `internal/substrate/vz/spawnvmhost_test.go`:
  `TestSpawnVMHost_DetachesIntoNewSession` — spawns a real child with the production
  `SysProcAttr{Setsid: true}` and asserts `Getsid(child) != Getsid(self)` and
  `Getsid(child) == child`. Fails against the old `Setpgid`-only attr.
- `cmd/rask/vmhost_darwin_test.go` (new):
  `TestVMHostSignals_IncludesSIGHUP` — asserts `vmHostSignals` contains exactly
  `{SIGTERM, SIGINT, SIGHUP}`.
- `internal/substrate/vz/vmhost_test.go`:
  `TestHandleVMStateChange` — table-driven over `Running`/`Paused`/`Starting` (non-terminal,
  `wantErr=false`) and `Stopped`/`Error` (terminal, `wantErr=true`); asserts the log line is
  always written regardless.

`GOWORK=off go build ./...`, `go vet`, `golangci-lint run` (0 issues), and
`go test -race -shuffle=on -count=1 ./internal/substrate/vz/... ./cmd/rask/...` all pass.
(`GOWORK=off` is required in this worktree — the main checkout's `go.work` only declares
`use .` and is auto-discovered from the nested worktree path otherwise, which breaks module
resolution; this is a worktree-location artifact, not a code issue.)

## Live VM verification (real Virtualization.framework VM, real fix, one VM, deleted after)

Built the fixed binary for real: `make build-rask-init` (cross-compile, CGO_ENABLED=0, never
executed on host) + `go build -o rask ./cmd/rask` + ad-hoc codesign with
`vz.entitlements` (`com.apple.security.virtualization`). Ran every step below with
`HOME=<scratchpad>/rask-live-verify-home` — fully isolated from the real `~/.rask`, cluster
named `vmhostfix` (not `dev`/`demo`). One VM, deleted at the end; `embedded/rask-init` reverted
to its committed placeholder afterward (`git checkout --`) so it isn't part of the commit.

1. `rask create cluster --name vmhostfix --verbose` → **succeeded**, node Ready, CoreDNS +
   local-path-provisioner Running, ~2 minutes wall time (first run on this host pays template
   initramfs/kernel cache cost).
2. Confirmed the fix live: `python3 -c 'os.getsid(pid)'` (macOS `ps -o sess=` prints a useless
   `0` for everything, not reliable) — vm-host `sid == 6790` (its own pid), my shell's
   `sid == 7797`. Distinct, as designed.
3. **Test A (kubectl port-forward killed)**: started a real `kubectl port-forward -n kube-system
   pod/coredns... 15353:53` in the background, sent it `SIGTERM` (mirroring the harness
   timeout). vm-host (pid 6790) stayed alive; `kubectl get nodes` still returned `Ready`
   afterward.
4. **Test B (originating shell/session terminated)**: spawned an isolated "harness session"
   stand-in (`os.fork()` + `os.setsid()` + `exec sleep`, its own session), sent `SIGHUP` to that
   **entire session** (`kill -HUP -- -<sid>`). The stand-in died. vm-host — confirmed to be in a
   completely different session — was unaffected.
5. **Test C (SIGHUP to a process group vm-host is not part of)**: same signal as #4, confirming
   vm-host is not reachable via arbitrary external session/group-targeted SIGHUP any more.
   Also recorded vm-host's own ancestry at this point: `PPID=1` — the original `rask create`
   CLI process and its shell were already long gone (ordinary orphan reparenting to launchd),
   and vm-host had been running fine the whole time regardless, which is itself additional
   organic evidence for this exact scenario.
6. `kubectl get nodes` after both B and C: still `Ready`.
7. `rask delete cluster --name vmhostfix` → **succeeded in 0.52s**: vm-host pid gone,
   `clusters/vmhostfix` directory gone, `clusters/` left empty, `vm.lock` left on disk with
   body `"vmhostfix"` (by design — the flock releases on process exit, the file body is
   informational only, matching `lock.go`'s documented contract). Confirmed no leaked
   `com.apple.Virtualization.VirtualMachine` XPC process for this cluster (the one such process
   still running on the host is colima's, PID 40583, running continuously since 14:30 with
   61+ CPU-minutes — pre-existing and unrelated).

All three reproduction scenarios the coordinator asked for were run against one real VM with
the fixed binary; vm-host survived all three, the cluster stayed responsive throughout, and the
normal `rask delete` teardown path was unaffected by the fix.

## What remains unverified

- **The exact mechanism that generated the original SIGHUP** (tty hangup vs. a harness sending
  a session/group-targeted signal when it killed the backgrounded `kubectl port-forward`) is
  not identified with certainty — I could not inspect the actual harness/script that ran the
  original reproduction. The fix is correct regardless of which mechanism it turns out to be,
  since it removes vm-host from session-based signal reachability entirely and additionally
  handles a direct SIGHUP gracefully.
- Whether the original incident's `tcpproxy:7777` log lines were boot-time `WaitHealthy` noise
  (now shown to be normal) or a genuine post-death recurrence cannot be re-checked — the
  original `vm-host.log` no longer exists.
- No diagnostic report could be positively attributed to rask's own Virtualization XPC process;
  the CPU-contention/hrtimer angle remains a plausible contributing factor (multiple concurrent
  VMs on the same host that afternoon, confirmed) but not a proven direct cause of this specific
  death.
- Whether jetsam killed anything in this window is inconclusive from the log queries run (no
  positive evidence found, but the search was not exhaustive of every possible predicate/term).
- Items (c)/(d) from the original task list — `rask get clusters` detecting/reporting a dead
  vm-host, and cleaning up a stale `vm.lock` body on detected death — were investigated (no bug
  found: `vm.lock`'s body is informational-only by design and never blocks correctness) but not
  implemented, since the coordinator's updated priority was fixing and proving the actual death
  cause first. Worth a follow-up if a dead vm-host with a stale pidfile should surface a clearer
  error than a raw `kubectl` connection-refused.
