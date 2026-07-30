//go:build linux

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// reapChildren reaps zombies for the lifetime of the guest. containerd's
// shim processes are deliberately double-forked to be reparented to PID 1
// so they survive containerd restarts, and every component
// internal/bootstrap.Supervisor launches is a direct child of this process.
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
