// Command rask-init is a minimal PID 1 for the rask microVM guest (spike S2).
//
// v0 responsibilities: mount the base pseudo-filesystems, print a
// boot-complete marker to the console, then idle while reaping zombie
// children via SIGCHLD.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func mount(source, target, fstype string, flags uintptr) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Printf("rask-init: mkdir %s: %v\n", target, err)
		return
	}
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		fmt.Printf("rask-init: mount %s on %s: %v\n", source, target, err)
	}
}

func main() {
	start := time.Now()

	mount("proc", "/proc", "proc", 0)
	mount("sysfs", "/sys", "sysfs", 0)
	mount("devtmpfs", "/dev", "devtmpfs", 0)

	fmt.Printf("RASK-INIT-BOOT-COMPLETE t=%s unixnano=%d\n", start.Format(time.RFC3339Nano), start.UnixNano())

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
