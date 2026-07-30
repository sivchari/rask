# Spike S5: vz memory snapshot save/restore — RESULTS

**Verdict: PASS.** Virtualization.framework VM memory snapshots deliver the
sub-second warm-resume primitive rask's v2 design needs.

## Numbers (5 cycles, 1 vCPU / 1GiB guest, S2 kernel+init, macOS 26.5)

| metric | range |
|---|---|
| SaveMachineStateToPath (paused VM) | 178-221ms (save file 10MiB) |
| RestoreMachineStateFromURL (fresh VM object) | 178-205ms |
| Resume() | ~0.1-0.16ms |
| restore start → first guest userspace output | 382-406ms |

A restored cluster would be interactive in **well under 0.5s** before any
Kubernetes state even needs touching — the v2 "<1s warm" target has ~600ms
of headroom.

## Findings that shape rask's design

1. **Machine identifier must be persisted.** A restored VM must be created
   with the SAME `VZGenericMachineIdentifier` as the saved one, or restore
   fails with VZErrorDomain Code=12 "invalid argument". rask must store the
   identifier alongside the snapshot (per cluster / per template).
2. **Call `ValidateSaveRestoreSupport()` before saving.** Without it,
   `SaveMachineStateToPath` failed with Code=11 "permission denied" (same
   binary, same entitlement). With the validation call in place, save
   succeeds. Treat it as a required precondition, not an optional check.
3. **The save file survives restore** on macOS 26.5 (observed 5/5), contrary
   to earlier reports that the framework deletes it. Repeated restores from
   one golden snapshot look feasible; still pair snapshots with CoW disk
   clones since disk state must match memory state.
4. **CLOCK_MONOTONIC does not jump or go backward**: guest monotonic time
   continues from the pause point (delta across a 3.6s host-side gap was
   ~200ms = one tick interval). Leases/timers in k8s components won't see a
   discontinuity — they simply experience "no time passed".
5. **Guest wall clock does NOT resync on restore**: it also continued from
   the pause point, staying stale by the host-side gap (~3.4s in this
   spike; would be hours for a parked cluster). Production rask MUST resync
   guest wall time after resume (host→guest time push over vsock/virtio, or
   the S3 `setClockFromCmdline` pattern generalized). Stale wall clock
   would break cert-validity checks and apiserver token expiry after long
   parks.
6. Serial FileHandle attachments and the entropy device pass
   `ValidateSaveRestoreSupport` and survive restore (console output resumes
   on the new VM object's pipes).

## Files
- main.go — host harness (save/restore cycle + clock instrumentation)
- init/main.go — guest PID 1 emitting monotonic/wall tick lines
