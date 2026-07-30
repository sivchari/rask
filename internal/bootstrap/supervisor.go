package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// defaultRestartDelay is used when a ProcessSpec enables RestartOnCrash but
// does not set RestartDelay.
const defaultRestartDelay = time.Second

// ErrAlreadyStarted is returned by Start when called on a Supervisor that
// has already been started.
var ErrAlreadyStarted = errors.New("bootstrap: supervisor already started")

// ProcessSpec describes one process for a Supervisor to launch.
type ProcessSpec struct {
	// Name identifies the process for Logs and error messages. It must
	// be unique within a single Start call.
	Name string

	// Path is the executable to run, and Args its arguments (not
	// including argv[0]).
	Path string
	Args []string

	// Env is the process environment. A nil Env means the process
	// inherits nothing beyond what the platform sets by default; unlike
	// os/exec, Env is never merged with the parent's environment.
	Env []string

	// Dir is the process's working directory. Empty means the caller's
	// current directory.
	Dir string

	// RestartOnCrash relaunches the process, after RestartDelay, any
	// time it exits with a non-zero status. It does not apply to a
	// clean (zero-status) exit or to Supervisor.Stop.
	RestartOnCrash bool

	// RestartDelay is how long to wait before relaunching a crashed
	// process. Defaults to defaultRestartDelay if zero.
	RestartDelay time.Duration

	// LogPath, if set, redirects the process's combined stdout/stderr
	// directly to this file (opened O_APPEND|O_CREATE) instead of the
	// in-memory buffer Logs() reads from.
	//
	// This matters for more than convenience: os/exec's default Writer
	// plumbing for a non-*os.File Stdout/Stderr copies through an
	// in-process pipe and a goroutine that belongs to the Go process
	// calling Launch/Start. For a process that must keep running after
	// that Go process exits (e.g. every component hostproc launches,
	// since "rask create" exits while the cluster keeps running), that
	// pipe and goroutine die with it — breaking, or eventually blocking
	// once the pipe buffer fills, the child's stdout/stderr writes.
	// Redirecting straight to a file avoids the Go-side plumbing
	// entirely: the OS-level file descriptor is inherited directly by
	// the child.
	LogPath string
}

// process is the runtime state Supervisor tracks for one ProcessSpec.
type process struct {
	spec ProcessSpec
	log  *logBuffer

	// cmd is the currently-running invocation (replaced on each
	// RestartOnCrash relaunch). Guarded by Supervisor.mu, since Stop's
	// PIDs snapshot and supervise's restart both touch it.
	cmd *exec.Cmd
}

// Supervisor launches a set of processes in order and keeps them running,
// restarting on crash where configured, until Stop is called.
type Supervisor struct {
	mu        sync.Mutex
	started   bool
	runCtx    context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	processes []*process
}

// NewSupervisor returns an unstarted Supervisor.
func NewSupervisor() *Supervisor {
	return &Supervisor{}
}

// Start launches specs in order, waiting for each to successfully start
// (fork+exec, not readiness) before launching the next. If any process
// fails to start, Start stops every process already launched in this call
// and returns an error identifying the one that failed.
//
// The processes keep running, supervised for restart-on-crash, until ctx is
// canceled or Stop is called.
func (s *Supervisor) Start(ctx context.Context, specs ...ProcessSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return ErrAlreadyStarted
	}

	s.started = true

	runCtx, cancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.cancel = cancel

	for _, spec := range specs {
		log := newLogBuffer()

		cmd, err := startProcess(runCtx, spec, log)
		if err != nil {
			cancel()
			s.wg.Wait()

			return fmt.Errorf("starting process %q: %w", spec.Name, err)
		}

		p := &process{spec: spec, log: log, cmd: cmd}
		s.processes = append(s.processes, p)

		s.wg.Add(1)

		go s.supervise(runCtx, p, cmd)
	}

	return nil
}

// Launch starts a single process and adds it to the Supervisor's managed
// set, supervising it (including RestartOnCrash) exactly like a process
// started via Start, until Stop is called.
//
// Unlike Start, which launches a fixed batch in one call and rejects being
// called a second time, Launch may be called any number of times to build
// up the running set incrementally as dependencies become ready. This is
// what a readiness-gated boot DAG needs: e.g. kube-apiserver must not be
// launched until kine's socket actually accepts connections, not merely
// after kine's fork+exec succeeds, so the caller cannot know every spec to
// launch up front the way Start requires.
//
// The first call to either Start or Launch on a Supervisor establishes the
// context every managed process is a child of; Stop terminates everything
// launched by both.
func (s *Supervisor) Launch(ctx context.Context, spec ProcessSpec) error {
	s.mu.Lock()

	if s.cancel == nil {
		runCtx, cancel := context.WithCancel(ctx)
		s.runCtx = runCtx
		s.cancel = cancel
	}

	runCtx := s.runCtx

	log := newLogBuffer()

	cmd, err := startProcess(runCtx, spec, log)
	if err != nil {
		s.mu.Unlock()

		return fmt.Errorf("bootstrap: launching process %q: %w", spec.Name, err)
	}

	p := &process{spec: spec, log: log, cmd: cmd}
	s.processes = append(s.processes, p)
	s.wg.Add(1)

	s.mu.Unlock()

	go s.supervise(runCtx, p, cmd)

	return nil
}

// supervise waits for cmd to exit and, while ctx is not done, relaunches it
// according to p.spec.RestartOnCrash.
func (s *Supervisor) supervise(ctx context.Context, p *process, cmd *exec.Cmd) {
	defer s.wg.Done()

	for {
		err := cmd.Wait()

		if ctx.Err() != nil {
			// Supervisor is stopping; Stop already killed the process.
			return
		}

		if err == nil || !p.spec.RestartOnCrash {
			return
		}

		delay := p.spec.RestartDelay
		if delay <= 0 {
			delay = defaultRestartDelay
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		next, startErr := startProcess(ctx, p.spec, p.log)
		if startErr != nil {
			_, _ = fmt.Fprintf(p.log, "bootstrap: restart failed: %v\n", startErr)

			return
		}

		s.mu.Lock()
		p.cmd = next
		s.mu.Unlock()

		cmd = next
	}
}

// Stop terminates every managed process and waits for their supervising
// goroutines to exit. It is safe to call on a Supervisor that was never
// started, and safe to call more than once.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	s.wg.Wait()
}

// PIDs returns the current OS process ID of every managed process, keyed by
// ProcessSpec.Name. This exists so a substrate implementation whose
// processes must outlive the CLI invocation that launched them (e.g.
// hostproc, where "rask create" exits while the cluster keeps running) can
// persist PIDs to disk and later kill them from a separate "rask delete"
// invocation that never had this Supervisor in memory.
func (s *Supervisor) PIDs() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	pids := make(map[string]int, len(s.processes))

	for _, p := range s.processes {
		if p.cmd != nil && p.cmd.Process != nil {
			pids[p.spec.Name] = p.cmd.Process.Pid
		}
	}

	return pids
}

// Logs returns the combined stdout/stderr captured so far for the named
// process (across restarts).
func (s *Supervisor) Logs(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range s.processes {
		if p.spec.Name == name {
			return p.log.Bytes(), nil
		}
	}

	return nil, fmt.Errorf("bootstrap: unknown process %q", name)
}

// startProcess starts spec as a child of ctx (canceling ctx kills the
// process) with its combined output captured into log, or redirected
// straight to spec.LogPath if set (see ProcessSpec.LogPath's doc comment
// for why that matters for processes that must outlive this Go process).
func startProcess(ctx context.Context, spec ProcessSpec, log *logBuffer) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir

	if spec.LogPath != "" {
		f, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("opening log file %s: %w", spec.LogPath, err)
		}
		// The child inherits its own duplicated file descriptor once
		// cmd.Start returns below, so it's safe to close our copy here.
		defer func() { _ = f.Close() }()

		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		cmd.Stdout = log
		cmd.Stderr = log
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return cmd, nil
}

// logBuffer is a concurrency-safe byte buffer used to capture a process's
// combined stdout/stderr while it may still be writing to it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newLogBuffer() *logBuffer {
	return &logBuffer{}
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *logBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]byte, l.buf.Len())
	copy(out, l.buf.Bytes())

	return out
}
