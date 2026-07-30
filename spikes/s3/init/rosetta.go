package main

import (
	"fmt"
	"os"
	"syscall"

	"rask-spike-s3/init/binfmt"
)

const (
	rosettaMount       = "/mnt/rosetta"
	rosettaInterpreter = rosettaMount + "/rosetta"
)

// mountRosetta mounts the host's VZLinuxRosettaDirectoryShare (virtiofs tag
// "rosetta") at /mnt/rosetta. The host only attaches this device when
// Rosetta is actually installed and available, so a mount failure here means
// the host didn't offer the share.
func mountRosetta() error {
	if err := os.MkdirAll(rosettaMount, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", rosettaMount, err)
	}
	if err := syscall.Mount("rosetta", rosettaMount, "virtiofs", 0, ""); err != nil {
		return fmt.Errorf("mount virtiofs rosetta: %w", err)
	}
	return nil
}

// registerBinfmt registers rosettaInterpreter as the binfmt_misc interpreter
// for x86-64 ELF binaries, so exec() of an amd64 binary transparently runs
// under Rosetta translation. See package binfmt for the magic/mask/flags
// semantics.
func registerBinfmt() error {
	registration := binfmt.AMD64ELFRegistration(rosettaInterpreter)

	f, err := os.OpenFile("/proc/sys/fs/binfmt_misc/register", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open binfmt_misc register: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(registration); err != nil {
		return fmt.Errorf("write binfmt_misc registration: %w", err)
	}
	return nil
}
