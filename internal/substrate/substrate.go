// Package substrate defines the interface rask uses to run a cluster
// instance on a given host: a Virtualization.framework VM on macOS
// (internal/substrate/vz), or supervised host processes in a network
// namespace on Linux (internal/substrate/hostproc).
package substrate

import (
	"context"
	"io"
)

// Runtime creates, controls and tears down one rask cluster instance on the
// host. Every method after Create takes the cluster name Create was called
// with.
//
// Implementations are not required to be safe for concurrent use by
// multiple goroutines operating on the same cluster name.
type Runtime interface {
	// Create prepares a cluster instance (e.g. a VM disk image or a
	// network namespace) but does not start it.
	Create(ctx context.Context, name string) error

	// Start boots the cluster instance created by Create.
	Start(ctx context.Context, name string) error

	// Stop shuts the cluster instance down without deleting its state,
	// so a later Start can resume it.
	Stop(ctx context.Context, name string) error

	// Delete removes a cluster instance and all of its state. It is an
	// error to call Delete on a cluster that is still running.
	Delete(ctx context.Context, name string) error

	// Exec runs command with args inside the cluster instance, streaming
	// its combined stdout/stderr to stdout, and returns the command's
	// exit code.
	Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (exitCode int, err error)

	// WriteFile writes data to path inside the cluster instance,
	// creating parent directories as needed.
	WriteFile(ctx context.Context, name string, path string, data []byte) error

	// PortForward forwards localAddr on the host to remoteAddr inside
	// the cluster instance until ctx is canceled or the returned error
	// channel is read from.
	PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (<-chan error, error)
}
