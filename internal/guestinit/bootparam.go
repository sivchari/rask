package guestinit

import (
	"fmt"
	"strconv"
	"strings"
)

// Kernel command-line parameter keys rask-init reads from /proc/cmdline.
// The host (internal/substrate/vz) sets these via vz.WithCommandLine.
// BootTimeUnixNano is needed because this kernel has no RTC, so it
// otherwise boots at the Unix epoch, breaking TLS certificate validation.
// ClusterName travels this way, rather than through a shared config file,
// as a deliberate per-cluster config transport simplification (see
// buildTemplateInitramfs's doc comment).
const (
	clusterNameParam = "rask.cluster="
	boottimeParam    = "rask.boottime="
)

// BootParams is every rask-specific kernel command-line parameter
// rask-init needs.
type BootParams struct {
	// ClusterName is the cluster instance name (bootstrap.Config.ClusterName).
	ClusterName string

	// BootTimeUnixNano is the host's wall-clock time, in UnixNano, taken
	// immediately before vm.Start(). rask-init applies it via
	// clock_settime very early in boot.
	BootTimeUnixNano int64
}

// ParseBootParams extracts rask's parameters from a /proc/cmdline-style
// space-separated string. Both rask.cluster= and rask.boottime= are
// required: a guest that can't determine its own cluster name or a
// plausible wall clock isn't safely bootable.
func ParseBootParams(cmdline string) (BootParams, error) {
	var (
		p       BootParams
		haveTS  bool
		haveClu bool
	)

	for _, field := range strings.Fields(cmdline) {
		switch {
		case strings.HasPrefix(field, clusterNameParam):
			p.ClusterName = strings.TrimPrefix(field, clusterNameParam)
			haveClu = true
		case strings.HasPrefix(field, boottimeParam):
			v := strings.TrimPrefix(field, boottimeParam)

			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return BootParams{}, fmt.Errorf("guestinit: parsing %s%s: %w", boottimeParam, v, err)
			}

			p.BootTimeUnixNano = n
			haveTS = true
		}
	}

	if !haveClu {
		return BootParams{}, fmt.Errorf("guestinit: missing required kernel parameter %q", clusterNameParam)
	}

	if !haveTS {
		return BootParams{}, fmt.Errorf("guestinit: missing required kernel parameter %q", boottimeParam)
	}

	return p, nil
}

// FormatBootParams renders p as kernel command-line fields, the inverse of
// ParseBootParams, for the host side (internal/substrate/vz) to append to
// vz.WithCommandLine.
func FormatBootParams(p BootParams) string {
	return fmt.Sprintf("%s%s %s%d", clusterNameParam, p.ClusterName, boottimeParam, p.BootTimeUnixNano)
}
