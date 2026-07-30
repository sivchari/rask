package main

import (
	"os"

	"golang.org/x/sys/unix"

	"rask-spike-s3/init/bootparam"
)

// setClockFromCmdline reads the rask.boottime= kernel command line
// parameter (set by the host harness to time.Now().UnixNano() just before
// vm.Start()) and applies it via clock_settime.
//
// puipui-linux's minimal guest has no RTC device, so without this the
// kernel boots with its clock at the Unix epoch. That's not just cosmetic:
// it breaks TLS certificate validation for anything the guest fetches
// (every cert's NotBefore is long after 1970-01-01), which includes the
// registry pull this spike exists to prove works. The host->guest clock
// handoff is a few hundred ms stale by the time this runs (whatever the VM
// boot latency is), which is negligible next to certificate validity
// windows.
func setClockFromCmdline() error {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	nanos, ok, err := bootparam.ParseBoottimeNanos(string(cmdline))
	if err != nil {
		return err
	}
	if !ok {
		return nil // no rask.boottime= param: leave the clock alone
	}
	ts := unix.NsecToTimespec(nanos)
	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}
