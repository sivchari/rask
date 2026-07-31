//go:build darwin

package vz

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/substrate"
)

// startLockFailFastBudget is generous headroom over the "a few
// milliseconds" a local flock-based check should actually take, so this
// test isn't flaky under CI/host load, while still comfortably catching a
// regression back to waitForVMState's old multi-minute
// wait-for-vm-host-to-exit-or-timeout behavior.
const startLockFailFastBudget = time.Second

// TestRuntime_Start_FailsFastWhenAnotherVMRunning is the regression test
// for the live incident this was written for: "rask create cluster --name
// t1" while another cluster's VM was already running used to hang for
// minutes (waitForVMState's bootTimeout) instead of failing immediately,
// because nothing checked the host-wide VM lock (lock.go) until the
// spawned vm-host process tried and silently exited. Start now peeks the
// lock itself, before spawning anything.
func TestRuntime_Start_FailsFastWhenAnotherVMRunning(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// Simulate cluster "dev"'s vm-host already holding the host-wide lock,
	// without needing a real VM or vm-host process.
	lock, err := acquireVMLock(homeDir, "dev")
	if err != nil {
		t.Fatalf("acquireVMLock: %v", err)
	}
	defer lock.Release()

	r := New(homeDir)

	start := time.Now()
	err = r.Start(context.Background(), "t1", substrate.StartOptions{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Start while another VM is running = nil error, want a lock-conflict error")
	}

	if !errors.Is(err, ErrAnotherVMRunning) {
		t.Errorf("Start error = %v, want it to wrap ErrAnotherVMRunning", err)
	}

	if !strings.Contains(err.Error(), `"dev"`) {
		t.Errorf("Start error = %q, want it to name the holder cluster %q", err.Error(), "dev")
	}

	if elapsed > startLockFailFastBudget {
		t.Errorf("Start took %s to fail on a lock conflict, want under %s", elapsed, startLockFailFastBudget)
	}

	if _, err := os.Stat(cluster.Dir(homeDir, "t1")); !os.IsNotExist(err) {
		t.Errorf("Start on a lock conflict created %s, want no state left behind (fails before any side effect)", cluster.Dir(homeDir, "t1"))
	}
}

// Deliberately no "lock free, Start proceeds past the check" test that
// calls Runtime.Start end-to-end: Start's next step past a free lock is
// spawnVMHost, which execs os.Executable() — inside `go test` that
// resolves to the compiled test binary itself. Passing it "__vm-host ..."
// as argv doesn't invoke rask's CLI at all (this is a test binary, not
// rask); since "__vm-host" doesn't start with "-", the testing package's
// flag.Parse stops there and the child just runs this entire test binary
// again from the top — a real, reproduced-live self-exec recursion, not a
// hypothetical. peekVMLock's own free-path is covered directly by
// TestPeekVMLock_FreeWhenUnlocked instead.
