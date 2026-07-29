package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// pollInterval is how often readiness probes are retried. Kept short since
// we're measuring sub-5-second boot latency; a coarse poll interval would
// itself become measurement noise.
const pollInterval = 20 * time.Millisecond

// managedProc is a child process started as part of the spike, tracked so
// it can be torn down as a whole process group (it and anything it forks,
// e.g. containerd's shims) at the end of a run.
type managedProc struct {
	name string
	cmd  *exec.Cmd
	log  *os.File
}

// startProc launches path with args, redirecting combined output to
// logDir/name.log, in its own process group so the whole subtree can be
// killed on teardown.
func startProc(name, path string, args []string, logDir string) (*managedProc, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	logFile, err := os.Create(filepath.Join(logDir, name+".log"))
	if err != nil {
		return nil, fmt.Errorf("creating log file for %s: %w", name, err)
	}

	cmd := exec.Command(path, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("starting %s: %w", name, err)
	}

	return &managedProc{name: name, cmd: cmd, log: logFile}, nil
}

// stop sends sig to the process group rooted at p, so forked descendants
// (e.g. containerd-shim) die too. It does not wait for exit.
func (p *managedProc) stop(sig syscall.Signal) {
	if p.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, sig)
}

// wait blocks until the process exits and closes its log file. Errors are
// expected and ignored here since teardown always sends a killing signal.
func (p *managedProc) wait() {
	_ = p.cmd.Wait()
	p.log.Close()
}

// waitUnixSocket polls until path exists and accepts a connection, or ctx
// is done.
func waitUnixSocket(ctx context.Context, path string) error {
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for unix socket %s: %w", path, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// waitHTTPOK polls url with client until it returns 2xx, or ctx is done.
func waitHTTPOK(ctx context.Context, client *http.Client, url string) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s to become healthy: %w", url, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
