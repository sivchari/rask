# vz-substrate fail-fast + XPC leak fixes — progress

Branch: `fix/vm-lock-failfast` (worktree: `.claude/worktrees/vm-lock-failfast`)

## Status: both fixes implemented, tested, lint/vet clean

## Bug 1 — fail-fast on lock conflict

- `internal/substrate/vz/lock.go`:
  - `acquireVMLock(homeDir, clusterName string)` now writes the holder's
    cluster name into the lock file body on success (`writeLockHolder`),
    and on conflict reads it back (`readLockHolder`) to name the holder in
    the returned error.
  - Added `peekVMLock(homeDir) (holder string, busy bool, err error)`:
    take-then-immediately-release a non-blocking flock, for the CLI side
    to check without becoming/holding the lock itself.
  - Added `lockConflictError(holder string) error`, producing exactly:
    `vz: another rask VM is already running (cluster "dev"); only one VM
    may run at a time — delete it first` (holder omitted if unknown).
  - Stale lock reclaim needs no special code: flock is kernel-released on
    process death regardless of file body content: only the body (purely
    cosmetic, for the error message) can go stale, never the exclusion
    itself.
- `internal/substrate/vz/vz.go`: `Runtime.Start` now calls `peekVMLock`
  as its very first step, before creating any cluster state or spawning
  vm-host, returning `lockConflictError` immediately on conflict.
- `internal/substrate/vz/vmhost.go`: `RunVMHost` passes `name` through to
  `acquireVMLock`.
- Tests: `lock_test.go` (updated + new: stale-content reclaim, peek
  free/busy, peek doesn't itself hold the lock, message content),
  `startlock_test.go` (new: `Runtime.Start` fails in <1s with a
  holder-naming error and leaves no cluster-dir side effect).

## Bug 2 — leaked Virtualization XPC process

- `internal/substrate/vz/vmhost.go`: added `runRecoverable`, wrapping both
  background goroutines (`logConsoleLines`, `runBootWatchdog`) with a
  `recover()` that calls `cancel()` on panic. Root cause: an unrecovered
  panic in *any* goroutine kills the whole process immediately, running
  only that goroutine's own defers — `RunVMHost`'s `defer stopVM(vm)` (in
  the main goroutine) would never run if a background goroutine panicked,
  leaking the VM's XPC child (which is parented by launchd, not vm-host,
  so it survives vm-host's death regardless of cause). Main-goroutine
  panics were already safe (defers run during that goroutine's own
  unwind); this closes the actual gap.
- `internal/substrate/vz/xpcleak.go` (new): warn-only leak detector for
  `Delete`.
  - No reliable parent-PID correlation exists (XPC process's parent is
    launchd, not vm-host) — documented in `leakedXPCPids`'s doc comment.
  - Correlates instead by open file descriptor: `lsof -p <pid>` against
    the cluster's `disk.img` path. This can't false-positive across
    clusters or against colima's own Virtualization VM (two VMs can't
    have the same disk file open), but can false-negative (permissions,
    already-closed fd) — so it only ever warns, never kills.
  - `ps`+`lsof` calls are bounded by a shared 5s `context.WithTimeout` so
    this diagnostic step can never block `Delete` from actually removing
    cluster state.
  - Wired into `Runtime.Delete` (after the "still running" check, before
    `RemoveAll`).
- Tests: `xpcleak_test.go` (pure parser tests for `ps`/`lsof` output,
  including the deleted-file-suffix edge case), `vmhost_test.go`
  (`runRecoverable` recovers + cancels on panic, doesn't cancel on normal
  return).

## Verification

- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`: clean.
- `go test -race -shuffle=on -count=1 ./...`: all packages pass.
- No live VM was launched for any of this.

## NOT done: live E2E check against the real "dev" cluster

The user asked, if safe, to verify `rask create --name poke` now fails
fast against their actually-running "dev" cluster on the real host. This
session's Bash tool turned out to execute inside Claude Code's own
sandboxed VM (`~/Library/Application Support/Claude/vm_bundles/
claudevm.bundle`, confirmed via `ps`/`lsof` on the one and only running
`com.apple.Virtualization.VirtualMachine` XPC process on this box), not
the user's literal host: `~/.rask` here has no `clusters/` dir and no
`dev` state at all. Running `rask create --name poke` from here would
exercise an isolated, empty `~/.rask`, not the real one with "dev"
running, so it would not actually validate the fix against the real
incident and was skipped rather than faked. If the user wants this
verified, they can run, on their real host:

```
time ./rask create cluster --name poke 2>&1 | tail -5
```

Expected: fails in well under a second with an error containing
`another rask VM is already running (cluster "dev")`. Cleanup (only if it
somehow doesn't fail fast and creates state): `./rask delete cluster
--name poke`.
