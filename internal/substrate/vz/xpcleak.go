//go:build darwin

package vz

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// virtualizationXPCMarker is the substring identifying a
// Virtualization.framework XPC helper process in `ps` output — see
// leakedXPCPids' doc comment for why this, rather than a parent-PID
// chain, is how a leaked VM process is found at all.
const virtualizationXPCMarker = "com.apple.Virtualization.VirtualMachine"

// xpcLeakCheckTimeout bounds the total time leakedXPCPids spends shelling
// out to ps and lsof (one ps call, plus one lsof call per candidate
// process it finds). This is a diagnostic, warn-only check — Delete's doc
// comment promises it never blocks actually removing the cluster's state,
// which a hung or merely slow `lsof` (it can genuinely stall on some
// filesystem types) would otherwise silently violate.
const xpcLeakCheckTimeout = 5 * time.Second

// leakedXPCPids returns the PIDs of any running Virtualization.framework
// XPC process that currently has diskPath open — i.e. one whose VM is (or
// very recently was) backed by that exact disk image, as opposed to some
// other Virtualization.framework consumer's VM running on the same host
// (colima runs one too; in principle so could another rask cluster, if
// the host-wide single-VM lock in lock.go were ever bypassed).
//
// Why this exists: a vz cluster's VM runs as a
// com.apple.Virtualization.VirtualMachine.xpc process launched via
// launchd's XPC service brokering, not as a child of vm-host (RunVMHost's
// own process) — confirmed live during this session, where such a
// process was found still running 23h after its vm-host had exited,
// consuming real CPU, unnoticed. Because launchd (not vm-host) is its
// parent, there is no parent-PID chain from a dead vm-host back to "its"
// XPC process for Delete to walk and confirm cleanup.
//
// Correlating by open file descriptor (via lsof) instead is reliable in
// the one way that matters here: two different VMs cannot both have the
// same disk image file open, so a match is never a false positive across
// clusters or against colima's VM. It can still false-negative — the
// process may have already closed the disk image, or lsof may be denied
// visibility into a process it doesn't have permission to inspect (e.g. a
// difference in sandboxing/entitlements between the caller and the XPC
// service) — so an empty result here is evidence of nothing, not proof
// there is no leak. Given that asymmetry, callers must only ever warn
// from this, never act automatically (see warnLeakedXPCProcesses): an
// unrelated process must never be killed on the strength of a heuristic
// that can miss real leaks but must not itself invent false ones either.
func leakedXPCPids(diskPath string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), xpcLeakCheckTimeout)
	defer cancel()

	psOut, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("running ps: %w", err)
	}

	var leaked []int

	for _, pid := range parseVirtualizationXPCPids(psOut) {
		lsofOut, err := exec.CommandContext(ctx, "lsof", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			// The process exited between ps and lsof, lsof was denied
			// visibility into it, or the shared ctx budget ran out —
			// none of these is evidence of a leak, so skip this
			// candidate rather than treat an unrelated failure as
			// suspicious.
			continue
		}

		if lsofHasOpenPath(lsofOut, diskPath) {
			leaked = append(leaked, pid)
		}
	}

	return leaked, nil
}

// parseVirtualizationXPCPids extracts every PID from `ps -axo
// pid=,command=` output (no header, "=" suppresses the column titles)
// whose command line contains virtualizationXPCMarker. Split out from
// leakedXPCPids so the parsing logic is testable without shelling out.
func parseVirtualizationXPCPids(psOutput []byte) []int {
	var pids []int

	scanner := bufio.NewScanner(bytes.NewReader(psOutput))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, virtualizationXPCMarker) {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		pids = append(pids, pid)
	}

	return pids
}

// lsofHasOpenPath reports whether any line of `lsof -p <pid>` output
// names path as an open file. Matches by substring rather than isolating
// lsof's NAME column and comparing it exactly: NAME is whitespace-
// delimited like every other column in lsof's default output, but isn't
// itself whitespace-free — a deleted-but-still-open file is reported as
// e.g. "/path/to/disk.img (deleted)", which would make a last-field
// extraction find "(deleted)" instead of the path. A plain substring
// match has none of that fragility for the one thing this needs to
// detect: whether path appears anywhere on the line at all. Split out
// from leakedXPCPids so the parsing logic is testable without shelling
// out.
func lsofHasOpenPath(lsofOutput []byte, path string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(lsofOutput))
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), path) {
			return true
		}
	}

	return false
}

// warnLeakedXPCProcesses logs (to stderr — this process's own stderr may
// itself be redirected to a log file by whatever spawned it, matching the
// existing best-effort-cleanup logging pattern in vz.go's Start) a
// warning naming any Virtualization XPC process that still has diskPath
// open after cluster name's Delete. It never kills anything: see
// leakedXPCPids' doc comment for why attribution here is a heuristic that
// can miss real leaks, which rules out ever treating a match (or the
// absence of one) as certain enough to act on automatically.
//
// Entirely best-effort: any error probing for leaked processes is itself
// only logged, never returned — this is purely diagnostic and must never
// block Delete from actually removing the cluster's state.
func warnLeakedXPCProcesses(name, diskPath string) {
	pids, err := leakedXPCPids(diskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vz: checking for leaked Virtualization XPC processes for cluster %q: %v\n", name, err)

		return
	}

	if len(pids) == 0 {
		return
	}

	fmt.Fprintf(os.Stderr,
		"vz: WARNING: cluster %q deleted, but %d Virtualization XPC process(es) still have its disk image open and were NOT killed automatically (attribution here is a heuristic, not certain enough to act on — verify manually before killing): %v\n",
		name, len(pids), pids)
}
