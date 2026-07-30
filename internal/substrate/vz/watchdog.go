//go:build darwin

package vz

import (
	"context"
	"time"
)

// bootWatchdogTimeout bounds how long RunVMHost waits for the guest to
// report healthy before stopping the VM and exiting on its own —
// independently of whether the "rask create" CLI process that spawned
// this vm-host is even still around to enforce its own bootTimeout.
//
// Found to matter live during this session: a vm-host process must not be
// able to keep an unbounded VM running forever just because its parent
// CLI process died (e.g. an interactive session restarting) before ever
// sending it a Stop signal — a vm-host with no self-imposed deadline is
// then the only thing left that could stop it, and it wasn't watching for
// this at all.
const bootWatchdogTimeout = 6 * time.Minute

// runBootWatchdog waits up to timeout for the guest's control agent
// (reachable through agentHostPort) to report healthy. If it never does —
// and ctx wasn't already canceled for some unrelated reason (e.g. a normal
// Stop) — it reports failure on failed (non-blocking send; failed must be
// buffered with capacity >= 1) and calls cancel to unblock RunVMHost's
// main wait loop, which then stops the VM.
//
// Must be run as its own goroutine once the VM has been told to start.
// Blocks until the guest becomes healthy, timeout elapses, or ctx is done
// for any other reason.
func runBootWatchdog(ctx context.Context, cancel context.CancelFunc, agentHostPort int, timeout time.Duration, failed chan<- struct{}) {
	client := newAgentClient(agentHostPort)

	watchCtx, watchCancel := context.WithTimeout(ctx, timeout)
	defer watchCancel()

	if err := client.WaitHealthy(watchCtx); err != nil && ctx.Err() == nil {
		// ctx.Err() == nil means the outer ctx (RunVMHost's own runCtx)
		// hasn't already been canceled for some other reason — only
		// then is this genuinely a boot timeout, not a race against an
		// ordinary shutdown that would also make WaitHealthy fail.
		select {
		case failed <- struct{}{}:
		default:
		}

		cancel()
	}
}
