//go:build linux

package hostproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// Exec runs command with args directly on the host and streams its
// combined stdout/stderr to stdout, returning its exit code. v1 hostproc
// has no isolation boundary between "the host" and "inside the cluster
// instance" (see package doc), so this is not sandboxed to name in any way
// beyond validating the cluster exists.
func (r *Runtime) Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (int, error) {
	if _, err := os.Stat(r.dataDir(name)); err != nil {
		return 0, fmt.Errorf("hostproc: cluster %q: %w", name, err)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stdout

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}

		return 0, fmt.Errorf("hostproc: running %s: %w", command, err)
	}

	return 0, nil
}

// WriteFile writes data to path on the host, creating parent directories
// as needed. v1 hostproc has no isolation boundary (see Exec's doc
// comment), so path is a literal host path, not translated in any way.
func (r *Runtime) WriteFile(_ context.Context, name string, path string, data []byte) error {
	if _, err := os.Stat(r.dataDir(name)); err != nil {
		return fmt.Errorf("hostproc: cluster %q: %w", name, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("hostproc: creating %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("hostproc: writing %s: %w", path, err)
	}

	return nil
}

// PortForward forwards localAddr to remoteAddr until ctx is canceled. Since
// v1 hostproc runs the cluster directly in the host's network namespace
// (see package doc), remoteAddr is already reachable from the host without
// forwarding; this exists to satisfy substrate.Runtime's interface
// uniformly across substrates (a future netns-isolated hostproc, or vz's
// VM, genuinely need it) and works correctly today as a plain TCP relay.
func (r *Runtime) PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (<-chan error, error) {
	if _, err := os.Stat(r.dataDir(name)); err != nil {
		return nil, fmt.Errorf("hostproc: cluster %q: %w", name, err)
	}

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("hostproc: listening on %s: %w", localAddr, err)
	}

	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		defer func() { _ = listener.Close() }()

		var wg sync.WaitGroup

		go func() {
			<-ctx.Done()
			_ = listener.Close()
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					wg.Wait()

					return
				}

				errCh <- fmt.Errorf("hostproc: accepting connection on %s: %w", localAddr, err)

				return
			}

			wg.Add(1)

			go func() {
				defer wg.Done()
				relay(ctx, conn, remoteAddr)
			}()
		}
	}()

	return errCh, nil
}

// relay copies data bidirectionally between conn and a new connection to
// remoteAddr until either side closes or ctx is done.
func relay(ctx context.Context, conn net.Conn, remoteAddr string) {
	defer func() { _ = conn.Close() }()

	dialer := &net.Dialer{}

	upstream, err := dialer.DialContext(ctx, "tcp", remoteAddr)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	// io.Copy below only returns once a Read/Write errors; it does not
	// itself observe ctx. Closing both connections when ctx is done is
	// what actually unblocks them on cancellation.
	stopWatching := make(chan struct{})
	defer close(stopWatching)

	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			_ = upstream.Close()
		case <-stopWatching:
		}
	}()

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, conn)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, upstream)
	}()

	wg.Wait()
}
