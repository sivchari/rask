//go:build darwin

package vz

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// minFreeMemoryMultiplier is how much free host memory must be available,
// relative to the VM's configured memory ceiling, before RunVMHost will
// boot a VM at all. Conservative rather than 1x: the host also needs
// headroom for its own workload (browsers, other VMs like colima, etc —
// added after a real host instability incident during E2E testing where
// the whole host became unstable while a VM was running) and for macOS's
// own memory-pressure handling to avoid triggering compressor/swap
// thrashing from a large new allocation.
const minFreeMemoryMultiplier = 1.5

// checkFreeMemory returns an error if the host's currently free host
// memory is less than minFreeMemoryMultiplier times requiredMiB. Called by
// RunVMHost before booting a VM.
func checkFreeMemory(requiredMiB uint64) error {
	freeMiB, err := hostFreeMemoryMiB()
	if err != nil {
		// vm_stat is a standard, always-present macOS tool; a failure
		// here means something unusual is wrong with the host, not
		// that memory is fine — fail closed rather than silently
		// skipping the check.
		return fmt.Errorf("vz: checking host free memory: %w", err)
	}

	return checkFreeMemoryFromStats(freeMiB, requiredMiB)
}

// checkFreeMemoryFromStats is checkFreeMemory's pure comparison, split out
// so the threshold logic is testable without shelling out to vm_stat.
func checkFreeMemoryFromStats(freeMiB, requiredMiB uint64) error {
	required := uint64(float64(requiredMiB) * minFreeMemoryMultiplier)
	if freeMiB < required {
		return fmt.Errorf("vz: insufficient free host memory to safely start a %dMiB VM: %dMiB free, want at least %dMiB (%.1fx headroom)",
			requiredMiB, freeMiB, required, minFreeMemoryMultiplier)
	}

	return nil
}

// hostFreeMemoryMiB runs vm_stat (the standard macOS memory statistics
// tool — Virtualization.framework itself exposes no equivalent API) and
// returns free+inactive pages converted to MiB. Inactive pages are readily
// reclaimable by the kernel under memory pressure, so they count as
// effectively free here — the same accounting Activity Monitor's own
// "Memory Pressure" gauge uses free+inactive as the healthy baseline.
func hostFreeMemoryMiB() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("running vm_stat: %w", err)
	}

	return parseVMStatFreeMiB(out)
}

// vmStatPageSizeRe matches vm_stat's header line, e.g.
// "Mach Virtual Memory Statistics: (page size of 16384 bytes)". The page
// size must be read from here, not assumed: it's 4096 on Intel Macs but
// 16384 on Apple Silicon.
var vmStatPageSizeRe = regexp.MustCompile(`page size of (\d+) bytes`)

// defaultVMStatPageSize is vm_stat's historical default (pre-Apple
// Silicon), used only if the header line is somehow missing or
// unparseable.
const defaultVMStatPageSize = 4096

func parseVMStatFreeMiB(output []byte) (uint64, error) {
	pageSize := uint64(defaultVMStatPageSize)

	var free, inactive uint64

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		if m := vmStatPageSizeRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseUint(m[1], 10, 64); err == nil {
				pageSize = v
			}

			continue
		}

		switch {
		case strings.HasPrefix(line, "Pages free:"):
			free = parseVMStatPageCount(line)
		case strings.HasPrefix(line, "Pages inactive:"):
			inactive = parseVMStatPageCount(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("reading vm_stat output: %w", err)
	}

	return (free + inactive) * pageSize / (1024 * 1024), nil
}

// parseVMStatPageCount extracts the trailing "<number>." field vm_stat
// prints on each "Pages ...:" line.
func parseVMStatPageCount(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0
	}

	last := strings.TrimSuffix(fields[len(fields)-1], ".")

	v, err := strconv.ParseUint(last, 10, 64)
	if err != nil {
		return 0
	}

	return v
}
