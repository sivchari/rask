// Command rask-init (spike S5) is a minimal PID 1 that prints a boot
// marker and then a monotonic+wall-clock tick every 200ms, so the host
// harness can observe clock behavior across a VM memory-snapshot
// save/restore cycle.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	start := time.Now()

	mount("proc", "/proc", "proc")
	mount("sysfs", "/sys", "sysfs")
	mount("devtmpfs", "/dev", "devtmpfs")

	fmt.Println("RASK-S5-BOOT-COMPLETE")

	go reapChildren()

	seq := 0
	for {
		// time.Since uses Go's monotonic reading; time.Now().Unix is wall.
		fmt.Printf("RASK-S5-TICK seq=%d mono_ms=%d wall_unix_ms=%d\n",
			seq, time.Since(start).Milliseconds(), time.Now().UnixMilli())
		seq++
		time.Sleep(200 * time.Millisecond)
	}
}

func mount(source, target, fstype string) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Printf("rask-init: mkdir %s: %v\n", target, err)
		return
	}
	if err := syscall.Mount(source, target, fstype, 0, ""); err != nil {
		fmt.Printf("rask-init: mount %s: %v\n", target, err)
	}
}

func reapChildren() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)
	for range ch {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}
