package main

import (
	"fmt"
	"os"
	"syscall"
)

// mount mounts source at target with fstype/flags, creating target first.
// Failures are logged to the console (our only output channel) but do not
// abort boot -- a spike prioritizes getting as far as possible and reporting
// exactly where it stopped over failing fast.
func mount(source, target, fstype string, flags uintptr, data string) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Printf("rask-init: mkdir %s: %v\n", target, err)
		return
	}
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		fmt.Printf("rask-init: mount %s on %s (%s): %v\n", source, target, fstype, err)
	}
}

// mountBase mounts the pseudo-filesystems every later stage depends on:
// proc (DHCP/netlink and containerd both read it), sysfs (binfmt_misc lives
// under it), devtmpfs (device nodes runc/containerd expect), and cgroup2
// (containerd's cgroupfs-driver default needs a mounted unified hierarchy).
func mountBase() {
	mount("proc", "/proc", "proc", 0, "")
	mount("sysfs", "/sys", "sysfs", 0, "")
	mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	mount("cgroup2", "/sys/fs/cgroup", "cgroup2", 0, "")
	mount("binfmt_misc", "/proc/sys/fs/binfmt_misc", "binfmt_misc", 0, "")

	for _, dir := range []string{
		"/etc/containerd",
		"/var/lib/containerd",
		"/run/containerd",
		"/mnt/rosetta",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Printf("rask-init: mkdir %s: %v\n", dir, err)
		}
	}
}
