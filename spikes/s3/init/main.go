// Command rask-init (spike S3) is a minimal PID 1 for the rask microVM
// guest that proves an arm64 vz guest can run amd64-only container images
// via Rosetta: mount the host's Rosetta directory share, register it as
// the amd64 binfmt_misc interpreter, bring up networking via DHCP, start
// containerd, pull a real amd64-only image, and run `uname -m` inside it.
//
// Every stage prints a RASK-S3-* marker line to the console (virtio
// console, our only output channel) so the host harness can measure
// per-stage latency without any other communication channel to the guest.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	boot := time.Now()

	mountBase()
	fmt.Printf("RASK-S3-MOUNTS-DONE t=%s\n", time.Since(boot))
	debugDumpSysFs()

	// The Alpine virt kernel ships virtio-net/virtiofs/binfmt_misc/overlay
	// as modules; everything downstream (Rosetta share, binfmt, DHCP,
	// containerd snapshotter) needs them in place first. binfmt_misc's
	// pseudo-fs can only be mounted once its module is live, so retry that
	// mount here (mountBase already handled the built-in case).
	if err := loadBootModules(); err != nil {
		fmt.Printf("RASK-S3-MODULES-FAILED err=%v\n", err)
	} else {
		fmt.Printf("RASK-S3-MODULES-LOADED t=%s\n", time.Since(boot))
		mount("binfmt_misc", "/proc/sys/fs/binfmt_misc", "binfmt_misc", 0, "")
	}

	go reapChildren()

	if err := setClockFromCmdline(); err != nil {
		fmt.Printf("RASK-S3-CLOCK-FAILED err=%v\n", err)
	} else {
		fmt.Printf("RASK-S3-CLOCK-SET now=%s\n", time.Now().Format(time.RFC3339))
	}

	if err := mountRosetta(); err != nil {
		fmt.Printf("RASK-S3-ROSETTA-MOUNT-FAILED err=%v\n", err)
	} else {
		fmt.Println("RASK-S3-ROSETTA-MOUNTED")
		if err := registerBinfmt(); err != nil {
			fmt.Printf("RASK-S3-BINFMT-FAILED err=%v\n", err)
		} else {
			fmt.Println("RASK-S3-BINFMT-REGISTERED")
		}
	}

	netStart := time.Now()
	netCtx, netCancel := context.WithTimeout(context.Background(), 15*time.Second)
	dhcp, err := bringUpNetwork(netCtx)
	netCancel()
	if err != nil {
		fmt.Printf("RASK-S3-NET-FAILED t=%s err=%v\n", time.Since(netStart), err)
		haltForInspection()
	}
	fmt.Printf("RASK-S3-NET-UP t=%s iface=%s ip=%s gw=%s dns=%v\n",
		time.Since(netStart), dhcp.iface, dhcp.ip, dhcp.gw, dhcp.dns)

	cdStart := time.Now()
	if _, err := startContainerd(); err != nil {
		fmt.Printf("RASK-S3-CONTAINERD-FAILED t=%s err=%v\n", time.Since(cdStart), err)
		haltForInspection()
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = waitForContainerdSocket(readyCtx)
	readyCancel()
	if err != nil {
		fmt.Printf("RASK-S3-CONTAINERD-FAILED t=%s err=%v\n", time.Since(cdStart), err)
		haltForInspection()
	}
	fmt.Printf("RASK-S3-CONTAINERD-READY t=%s\n", time.Since(cdStart))

	pullStart := time.Now()
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 120*time.Second)
	out, err := pullTestImage(pullCtx)
	pullCancel()
	if err != nil {
		fmt.Printf("RASK-S3-PULL-FAILED t=%s err=%v out=%s\n", time.Since(pullStart), err, oneLine(out))
		haltForInspection()
	}
	fmt.Printf("RASK-S3-PULL-DONE t=%s\n", time.Since(pullStart))

	runStart := time.Now()
	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	out, err = runTestContainer(runCtx, "s3demo")
	runCancel()
	if err != nil {
		fmt.Printf("RASK-S3-RUN-FAILED t=%s err=%v out=%s\n", time.Since(runStart), err, oneLine(out))
		haltForInspection()
	}
	fmt.Printf("RASK-S3-RUN-DONE t=%s uname=%s\n", time.Since(runStart), oneLine(out))

	fmt.Printf("RASK-S3-ALL-DONE t=%s\n", time.Since(boot))

	haltForInspection()
}

// oneLine collapses command output to a single line so it can't break a
// console-log-based marker parser on the host.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// haltForInspection idles forever (reaping zombies) instead of returning
// from main, which would panic PID 1. Used both on the success path (so the
// host has time to read the final marker before stopping the VM) and on any
// failure path (so the console log up to the failure stays available).
func haltForInspection() {
	select {}
}

// reapChildren reaps zombies for the lifetime of the guest: containerd's
// shim processes are deliberately double-forked to be reparented to PID 1
// so they survive containerd restarts, and containerd itself is our direct
// child.
func reapChildren() {
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD)
	for range sigs {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}
