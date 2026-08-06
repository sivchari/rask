//go:build linux

package cluster

import "testing"

// TestRunVMHostIfRequested_AlwaysNoop proves RunVMHostIfRequested is a
// true no-op on Linux regardless of os.Args' content: hostproc has no
// detached-VM entrypoint for "__vm-host" to back, so even an argv shaped
// exactly like the macOS entrypoint's invocation must not be treated as
// handled here.
func TestRunVMHostIfRequested_AlwaysNoop(t *testing.T) {
	t.Parallel()

	handled, err := RunVMHostIfRequested()
	if handled {
		t.Error("RunVMHostIfRequested() handled = true on Linux, want false")
	}

	if err != nil {
		t.Errorf("RunVMHostIfRequested() err = %v, want nil", err)
	}
}
