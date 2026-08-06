//go:build linux

package cluster

// RunVMHostIfRequested always returns (false, nil) on Linux:
// internal/substrate/hostproc runs every cluster component directly on the
// host and has no detached-VM entrypoint for "__vm-host" to back (see
// internal/substrate/vz's package doc for why macOS needs one at all), so
// there is nothing here to intercept. A consumer can still call this
// unconditionally at the top of main on every platform, with no build tag
// of its own — see vmhost_darwin.go's doc comment for the macOS behavior
// this mirrors.
func RunVMHostIfRequested() (handled bool, err error) {
	return false, nil
}
