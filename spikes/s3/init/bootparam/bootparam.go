// Package bootparam parses rask-init's custom kernel command-line
// parameters. Pure string parsing, no OS dependency, so it can be built and
// tested on any host unlike the rest of rask-init.
package bootparam

import (
	"fmt"
	"strconv"
	"strings"
)

// BoottimeParam is the kernel cmdline key the host harness sets to
// time.Now().UnixNano() just before vm.Start(), so the guest can seed its
// otherwise-epoch clock. See init/clock.go for why this is needed.
const BoottimeParam = "rask.boottime="

// ParseBoottimeNanos extracts the rask.boottime= value from a
// /proc/cmdline-style space-separated parameter string. ok is false if the
// parameter isn't present.
func ParseBoottimeNanos(cmdline string) (nanos int64, ok bool, err error) {
	for _, field := range strings.Fields(cmdline) {
		v, found := strings.CutPrefix(field, BoottimeParam)
		if !found {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("parse %s%s: %w", BoottimeParam, v, err)
		}
		return n, true, nil
	}
	return 0, false, nil
}
