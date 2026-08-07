//go:build linux

// Command rask-init is PID 1 inside a rask vz guest: it mounts the base
// pseudo-filesystems, loads kernel modules, switches to a tmpfs root
// (the M0 s3 spike's "--no-pivot" workaround was spike-only — production
// runc needs a real mount point to pivot_root into), formats and mounts the
// per-cluster data disk, brings up static networking against
// gvisor-tap-vsock, mounts Rosetta and registers binfmt_misc, then runs
// internal/bootstrap.Boot (the same DAG internal/substrate/hostproc uses on
// Linux) to bring the node up. Once boot succeeds it serves a minimal HTTP
// control agent (internal/guestagent) so the host can retrieve the admin
// kubeconfig and satisfy substrate.Runtime's Exec/WriteFile.
//
// No shell, no busybox: every stage is plain Go over syscalls/netlink,
// matching the approach the M0 s2-s4 spikes established. Every stage prints a
// RASK-INIT-* marker line to the console (virtio console, the only output
// channel available before networking exists) so the host can observe boot
// progress and, during development, diagnose exactly where a boot stalled.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sivchari/rask/internal/guestconfig"
	"github.com/sivchari/rask/internal/guestinit"
	"github.com/sivchari/rask/internal/guestlayout"
)

func main() {
	boot := time.Now()

	mountBase()
	fmt.Printf("RASK-INIT-MOUNTS-DONE t=%s\n", time.Since(boot))

	go reapChildren()

	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		fatalf("reading /proc/cmdline: %v", err)
	}

	params, err := guestinit.ParseBootParams(string(cmdline))
	if err != nil {
		fatalf("parsing boot params: %v", err)
	}

	if err := setClock(params.BootTimeUnixNano); err != nil {
		fmt.Printf("RASK-INIT-CLOCK-FAILED err=%v\n", err)
	} else {
		fmt.Printf("RASK-INIT-CLOCK-SET now=%s\n", time.Now().Format(time.RFC3339))
	}

	modStart := time.Now()

	if err := loadModules(); err != nil {
		fatalf("loading kernel modules: %v", err)
	}

	fmt.Printf("RASK-INIT-MODULES-LOADED t=%s\n", time.Since(modStart))

	if err := switchRoot(); err != nil {
		fatalf("switching root: %v", err)
	}

	// Must run before the second mountBase() call below: makeRootShared
	// marks "/" MS_REC|MS_SHARED, and any mount created under an already-
	// shared mountpoint automatically joins its peer group — doing this
	// first means /proc, /sys, /dev etc. (and everything mounted later,
	// e.g. the data disk) all end up shared too, not just "/" itself.
	if err := makeRootShared(); err != nil {
		fmt.Printf("RASK-INIT-ROOT-SHARED-FAILED err=%v\n", err)
	}

	// proc/sys/dev were mounted at the initramfs's paths, which are no
	// longer reachable after chroot (they were never moved, only the
	// tmpfs root itself was) — mount them again fresh under the new
	// root.
	mountBase()
	fmt.Printf("RASK-INIT-SWITCH-ROOT-DONE t=%s\n", time.Since(boot))

	if err := formatAndMountDataDisk(); err != nil {
		fatalf("mounting data disk: %v", err)
	}

	fmt.Printf("RASK-INIT-DATA-DISK-READY t=%s\n", time.Since(boot))

	// Must run after formatAndMountDataDisk: see copyPrebootFiles' doc
	// comment for why guestlayout.PrebootDir is only visible once the data
	// disk is mounted over DataMountPoint.
	if err := copyPrebootFiles(); err != nil {
		fatalf("staging preboot files: %v", err)
	}

	setGuestPath()

	netStart := time.Now()

	if err := configureNetwork(); err != nil {
		fatalf("configuring network: %v", err)
	}

	fmt.Printf("RASK-INIT-NET-READY t=%s\n", time.Since(netStart))

	// Rosetta mount + binfmt_misc registration is deliberately NOT called
	// here — see rosetta.go's mountRosettaAndRegisterBinfmt doc comment
	// for why (a suspected correlation with a real host crash during E2E
	// testing, disabled at the user's explicit direction). Only arm64
	// guest images work for now.

	// guestCfg carries StartOptions fields internal/substrate/vz has no
	// other transport for (see internal/guestconfig's package doc). A
	// missing file (the common case: no --apiserver-arg/--coredns-image
	// was passed) is not an error — Load returns a zero Config.
	guestCfg, err := guestconfig.Load(guestlayout.ClusterConfigPath)
	if err != nil {
		fatalf("loading cluster config: %v", err)
	}

	bootStart := time.Now()

	result, err := runBoot(context.Background(), params.ClusterName, guestCfg)
	if err != nil {
		fmt.Printf("RASK-INIT-BOOT-FAILED t=%s err=%v\n", time.Since(bootStart), err)
		serveAgent(nil) // still serve /healthz (always 503) so the host's boot-timeout path gets a clean connection-refused-free signal instead of hanging.

		return
	}

	fmt.Printf("RASK-INIT-BOOT-READY t=%s total=%s\n", time.Since(bootStart), time.Since(boot))

	serveAgent(result)
}

// fatalf prints a failure marker and halts forever (reaping zombies via the
// already-running reapChildren goroutine) instead of returning from main,
// which would panic PID 1. Used for failures early enough that nothing
// useful can run afterward (e.g. no root filesystem, no modules).
func fatalf(format string, args ...any) {
	fmt.Printf("RASK-INIT-FATAL "+format+"\n", args...)
	select {}
}
