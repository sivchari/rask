// Package kine implements store.Datastore by supervising a kine process
// (https://github.com/k3s-io/kine), which speaks the etcd gRPC API on top
// of an embedded SQLite database. This is rask's default datastore: it
// avoids running a real etcd for a single-node local cluster.
package kine

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
	"syscall"
	"time"

	"github.com/sivchari/rask/internal/store"
)

// pollInterval is how often Start polls for kine's unix socket to accept
// connections.
const pollInterval = 20 * time.Millisecond

// stopTimeout is how long Stop waits for a graceful SIGTERM exit — long
// enough for kine to checkpoint SQLite's WAL into the main db file — before
// escalating to SIGKILL.
const stopTimeout = 5 * time.Second

// Datastore implements store.Datastore by supervising a kine process
// listening on a unix socket, backed by a SQLite file, both under dataDir.
type Datastore struct {
	binPath string
	dataDir string
	// extraEnv is appended to the spawned process's environment (which
	// otherwise matches this process's own, as with a nil exec.Cmd.Env).
	// It exists so tests can re-exec the test binary itself as a stand-in
	// kine process without mutating this process's real environment
	// (which would race with other parallel tests).
	extraEnv []string

	mu  sync.Mutex
	cmd *exec.Cmd
	log *os.File
}

var _ store.Datastore = (*Datastore)(nil)

// New returns a kine-backed Datastore that runs binPath and stores its
// SQLite database and unix socket under dataDir.
func New(binPath, dataDir string) *Datastore {
	return &Datastore{binPath: binPath, dataDir: dataDir}
}

// PID returns the OS process ID of the running kine process, or false if
// Start has not (yet, or successfully) been called. It exists so a
// substrate implementation whose processes must outlive the CLI invocation
// that launched them (e.g. hostproc) can persist it to disk alongside
// bootstrap.Supervisor's PIDs, for a later "rask delete" invocation to kill.
func (d *Datastore) PID() (int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cmd == nil || d.cmd.Process == nil {
		return 0, false
	}

	return d.cmd.Process.Pid, true
}

func (d *Datastore) socketPath() string {
	return filepath.Join(d.dataDir, "kine.sock")
}

func (d *Datastore) dbPath() string {
	return filepath.Join(d.dataDir, "state.db")
}

// Start launches kine and blocks until its unix socket accepts connections
// (or ctx is done), returning the "unix://" endpoint the API server should
// connect to as --etcd-servers.
func (d *Datastore) Start(ctx context.Context) (string, error) {
	d.mu.Lock()
	if d.cmd != nil {
		d.mu.Unlock()

		return "", errors.New("kine: already started")
	}

	if err := os.MkdirAll(d.dataDir, 0o755); err != nil {
		d.mu.Unlock()

		return "", fmt.Errorf("kine: creating data dir %s: %w", d.dataDir, err)
	}

	logFile, err := os.Create(filepath.Join(d.dataDir, "kine.log"))
	if err != nil {
		d.mu.Unlock()

		return "", fmt.Errorf("kine: creating log file: %w", err)
	}

	cmd := exec.Command(d.binPath,
		"--listen-address", "unix://"+d.socketPath(),
		"--endpoint", "sqlite://"+d.dbPath()+"?_journal=WAL&cache=shared",
		"--metrics-bind-address", "0",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if len(d.extraEnv) > 0 {
		cmd.Env = append(os.Environ(), d.extraEnv...)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		d.mu.Unlock()

		return "", fmt.Errorf("kine: starting process: %w", err)
	}

	d.cmd = cmd
	d.log = logFile
	d.mu.Unlock()

	if err := waitUnixSocket(ctx, d.socketPath()); err != nil {
		_ = d.Stop(context.Background())

		return "", fmt.Errorf("kine: waiting for %s to become ready: %w", d.socketPath(), err)
	}

	return "unix://" + d.socketPath(), nil
}

// Stop terminates kine (SIGTERM, so SQLite checkpoints its WAL into the main
// db file cleanly, then SIGKILL if it does not exit within stopTimeout) and
// waits for it to exit. It is a no-op if kine was never started or has
// already been stopped.
func (d *Datastore) Stop(ctx context.Context) error {
	d.mu.Lock()
	cmd := d.cmd
	log := d.log
	d.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var stopErr error

	select {
	case <-done:
	case <-time.After(stopTimeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done

		stopErr = ctx.Err()
	}

	if log != nil {
		_ = log.Close()
	}

	d.mu.Lock()
	d.cmd = nil
	d.log = nil
	d.mu.Unlock()

	return stopErr
}

// SeedFrom copies the prebaked SQLite file at path into dataDir/state.db,
// so the next Start skips cluster bootstrap. It must be called before
// Start.
func (d *Datastore) SeedFrom(_ context.Context, path string) error {
	d.mu.Lock()
	started := d.cmd != nil
	d.mu.Unlock()

	if started {
		return errors.New("kine: SeedFrom must be called before Start")
	}

	if err := os.MkdirAll(d.dataDir, 0o755); err != nil {
		return fmt.Errorf("kine: creating data dir %s: %w", d.dataDir, err)
	}

	if err := copyFile(path, d.dbPath()); err != nil {
		return fmt.Errorf("kine: seeding from %s: %w", path, err)
	}

	return nil
}

// waitUnixSocket polls until path exists and accepts a connection, or ctx
// is done. The timeout error includes the last dial error observed, not
// just ctx.Err(), so a caller can tell what was actually wrong (e.g. "no
// such file" vs. some other failure) instead of just "context deadline
// exceeded".
func waitUnixSocket(ctx context.Context, path string) error {
	var lastErr error

	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()

			return nil
		}

		lastErr = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last dial error: %v)", ctx.Err(), lastErr)
		case <-time.After(pollInterval):
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dst, err)
	}

	return nil
}
