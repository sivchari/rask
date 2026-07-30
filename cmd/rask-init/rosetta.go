//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/sivchari/rask/internal/guestinit"
	"github.com/sivchari/rask/internal/guestlayout"
)

const rosettaInterpreter = guestlayout.RosettaMount + "/rosetta"

// mountRosettaAndRegisterBinfmt mounts the host's
// VZLinuxRosettaDirectoryShare (virtiofs tag "rosetta") and registers it as
// the amd64 binfmt_misc interpreter, letting containerd run amd64-only
// images transparently (spikes/s3's production recipe).
//
// NOT CALLED right now (see disabledFeatures below): the host side
// (internal/substrate/vz/vm.go's attachRosetta) no longer attaches this
// share at all, disabled at the user's explicit direction after E2E
// testing on this exact machine correlated with repeated system-wide host
// crashes. Kept here for when that's revisited, not deleted. No enable
// flag exists — re-enabling requires an explicit code change on both the
// host and guest sides.
func mountRosettaAndRegisterBinfmt() error {
	if err := os.MkdirAll(guestlayout.RosettaMount, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", guestlayout.RosettaMount, err)
	}

	if err := syscall.Mount("rosetta", guestlayout.RosettaMount, "virtiofs", 0, ""); err != nil {
		return fmt.Errorf("mount virtiofs rosetta: %w", err)
	}

	registration := guestinit.AMD64ELFRegistration(rosettaInterpreter)

	f, err := os.OpenFile("/proc/sys/fs/binfmt_misc/register", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open binfmt_misc register: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(registration); err != nil {
		return fmt.Errorf("write binfmt_misc registration: %w", err)
	}

	return nil
}

// disabledFeatures keeps mountRosettaAndRegisterBinfmt reachable (so it
// compiles and isn't flagged as dead code) without actually calling it
// from anywhere at runtime — see its doc comment for why it's disabled.
var disabledFeatures = struct {
	mountRosettaAndRegisterBinfmt func() error
}{
	mountRosettaAndRegisterBinfmt: mountRosettaAndRegisterBinfmt,
}
