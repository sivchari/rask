//go:build linux

package main

import "golang.org/x/sys/unix"

// setClockFromCmdline sets the guest's wall clock via clock_settime,
// from a value the host handed over on the kernel command line — see
// guestinit.BootParams.BootTimeUnixNano's doc comment for why this is
// needed (no RTC in this guest, so TLS certificate validation would
// otherwise fail against an epoch-zero clock).
func setClock(bootTimeUnixNano int64) error {
	ts := unix.NsecToTimespec(bootTimeUnixNano)

	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}
