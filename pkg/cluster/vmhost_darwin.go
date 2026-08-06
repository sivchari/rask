//go:build darwin

package cluster

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sivchari/rask/internal/substrate/vz"
)

// vmHostSignals are the signals that trigger vm-host's graceful shutdown
// path (context cancellation, unblocking vz.RunVMHost's own internal
// ctx.Done() case so its deferred VM/console/network/lock cleanup actually
// runs).
//
// SIGHUP is included alongside the obvious SIGTERM/SIGINT as defense in
// depth: internal/substrate/vz.Runtime's spawnVMHost detaches this process
// into its own session (Setsid) specifically so it is no longer reachable
// by its spawning session's own signal delivery, but Setsid cannot protect
// against a SIGHUP sent directly to this process (e.g. an operator's own
// "kill -HUP", or any other future caller of syscall.Kill). Go's default
// disposition for an unhandled SIGHUP is to terminate the process
// immediately — no deferred cleanup, no log line — which is exactly the
// silent-death shape a real incident had (see spawnVMHost's doc comment
// for the full incident writeup). Catching it here routes it through the
// same clean shutdown as SIGTERM/SIGINT instead.
var vmHostSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// RunVMHostIfRequested must be called as early as possible in main — before
// the calling program parses its own flags or subcommands — by any program
// that constructs a Provider on macOS: internal/substrate/vz's Runtime
// hosts each cluster's VM in a detached child process that outlives the
// "rask create" invocation which spawned it, and spawns that child by
// re-execing the currently running binary with the hidden "__vm-host"
// entrypoint (see that package's doc comment). Without this call, a
// consumer's own binary has no matching entrypoint, so the spawned process
// exits immediately and Provider.Create fails after Provider.Create's own
// boot timeout with an opaque "waiting for vm-host to report its network
// state: context deadline exceeded" instead of ever getting the chance to
// host the VM.
//
// If os.Args requests the entrypoint, RunVMHostIfRequested parses it,
// blocks for the VM's entire lifetime (returning when it shuts down,
// gracefully or otherwise), and returns handled=true with whatever error
// vz.RunVMHost itself reported (nil on a clean shutdown). The caller's
// main should treat a non-nil error as fatal (log it, exit non-zero) and,
// either way, must not fall through to the rest of its own startup: this
// process's only job for its entire life is hosting one cluster's VM.
//
// If os.Args does not request the entrypoint (the overwhelmingly common
// case — an ordinary invocation of the consumer's own program),
// RunVMHostIfRequested returns (false, nil) immediately, so it is safe —
// intended — to call unconditionally as the very first line of main, with
// no build tag of its own: RunVMHostIfRequested compiles and returns
// (false, nil) on Linux too (see vmhost_linux.go), where
// internal/substrate/hostproc has no detached-VM concept to back it.
func RunVMHostIfRequested() (handled bool, err error) {
	home, name, ok, parseErr := parseVMHostArgs(os.Args)
	if !ok {
		return false, nil
	}

	if parseErr != nil {
		return true, parseErr
	}

	ctx, cancel := signal.NotifyContext(context.Background(), vmHostSignals...)
	defer cancel()

	return true, vz.RunVMHost(ctx, home, name)
}
