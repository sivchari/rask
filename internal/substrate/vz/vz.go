//go:build darwin

// Package vz implements substrate.Runtime on top of macOS's
// Virtualization.framework (via code-hex/vz): one rask cluster is one
// lightweight Linux VM.
//
// Unlike internal/substrate/hostproc (which runs every component directly
// on the host), vz has no shared filesystem with its cluster: the guest's
// data disk is a virtio-blk block device the host cannot read while the
// guest owns it, and there is no host process to attach to directly. Two
// things bridge that gap:
//
//   - rask-init (cmd/rask-init), the guest's PID 1, doubles as a minimal
//     HTTP control agent (internal/guestagent) reachable through a
//     gvisor-tap-vsock port forward, for fetching the kubeconfig/timeline
//     and for Exec/WriteFile.
//   - the VM itself runs in a detached child process ("rask __vm-host",
//     see vmhost.go), spawned by Start and outliving the "rask create"
//     invocation that spawned it — Stop/Delete are separate CLI
//     invocations with no in-memory record of it, so its PID and
//     gvisor-tap-vsock forwarded ports are persisted to disk (state.go)
//     for them to read back.
package vz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// bootTimeout bounds how long Start waits for the guest agent to report
// healthy (node Ready + CoreDNS/local-path-provisioner applied — see
// cmd/rask-init/boot.go) before giving up. Generous: the first Start for a
// given host also pays the one-time cost of building the (thereafter
// cached) template initramfs, on top of the VM's own boot and Kubernetes
// bootstrap time.
const bootTimeout = 5 * time.Minute

// Runtime implements substrate.Runtime using a Virtualization.framework VM
// per cluster.
type Runtime struct {
	homeDir string
}

var _ substrate.Runtime = (*Runtime)(nil)

// New returns a Runtime for the macOS vz substrate, storing every
// cluster's state under homeDir (see internal/cluster.Dir).
func New(homeDir string) *Runtime {
	return &Runtime{homeDir: homeDir}
}

func (r *Runtime) dataDir(name string) string {
	return filepath.Join(cluster.Dir(r.homeDir, name), "data")
}

func (r *Runtime) pidPath(name string) string {
	return filepath.Join(cluster.Dir(r.homeDir, name), "vm-host.pid")
}

func (r *Runtime) kubeconfigPath(name string) string {
	return filepath.Join(cluster.Dir(r.homeDir, name), "kubeconfig")
}

func (r *Runtime) timelinePath(name string) string {
	return filepath.Join(r.dataDir(name), "timeline.txt")
}

// Create prepares (but does not start) a cluster instance: ensures the
// template initramfs and guest kernel are cached (the first call on a
// fresh host pays this download/build cost; later calls are instant), and
// creates the per-cluster virtio-blk data disk file. Does not touch
// cluster.Dir(homeDir, name) itself for the same reason
// internal/substrate/hostproc.Create doesn't: a failed Create should leave
// no trace that would block a retried "rask create".
func (r *Runtime) Create(ctx context.Context, name string) error {
	if embedded.IsPlaceholder() {
		return errors.New("vz: internal/substrate/vz/embedded/rask-init is still the placeholder: run `make build-rask-init` first")
	}

	cache := components.NewCache(filepath.Join(r.homeDir, "cache"))

	if _, err := buildTemplateInitramfs(ctx, cache); err != nil {
		return fmt.Errorf("vz: preparing template initramfs: %w", err)
	}

	return nil
}

// Start spawns the cluster's VM as a detached "rask __vm-host" child
// process (see package doc), waits for the guest to report healthy through
// its control agent, then persists the PID, admin kubeconfig (rewritten to
// point at the forwarded host port) and boot timeline for later
// Stop/Delete/--verbose calls.
//
// If anything after the child process starts subsequently fails, Start
// gracefully terminates it (terminateVMHost — SIGTERM first, so
// RunVMHost's own cleanup actually stops the underlying VM instead of
// leaving it orphaned; see terminateVMHost's doc comment) and removes
// cluster.Dir(homeDir, name) entirely before returning, mirroring
// internal/substrate/hostproc.Start's failure-cleanup contract. The pid is
// captured directly in this closure (not re-read from the pidfile) so
// cleanup still finds and terminates the process even if the pidfile write
// itself is what failed.
// opts is currently unused: extra API audiences are threaded through the
// hostproc substrate (see internal/substrate/hostproc.Runtime.Start); vz's
// guest-side boot path does not yet plumb Config.ExtraAPIAudiences through
// to the in-guest bootstrap.Boot call.
func (r *Runtime) Start(ctx context.Context, name string, _ substrate.StartOptions) (err error) {
	clusterDir := cluster.Dir(r.homeDir, name)
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", clusterDir, err)
	}

	var pid int

	defer func() {
		if err != nil {
			if pid > 0 {
				terminateVMHost(context.Background(), pid, vmHostGracePeriod)
			}

			_ = os.RemoveAll(clusterDir)
		}
	}()

	pid, err = r.spawnVMHost(name)
	if err != nil {
		return err
	}

	if err = os.WriteFile(r.pidPath(name), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("vz: writing pidfile: %w", err)
	}

	bootCtx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()

	state, err := r.waitForVMState(bootCtx, name, pid)
	if err != nil {
		return err
	}

	client := newAgentClient(state.AgentHostPort)
	if err := client.WaitHealthy(bootCtx); err != nil {
		return fmt.Errorf("vz: waiting for guest to become healthy: %w", err)
	}

	if err := r.fetchKubeconfig(bootCtx, client, name, state.HostPort); err != nil {
		return err
	}

	if err := r.fetchTimeline(bootCtx, client, name); err != nil {
		return err
	}

	return nil
}

// spawnVMHost execs the currently-running rask binary as
// "rask __vm-host --name <name>", detached from the current process group
// (Setpgid) so it survives both this CLI invocation exiting and any
// terminal signal (e.g. Ctrl-C) delivered to this invocation's process
// group while Start is still waiting for boot to finish.
func (r *Runtime) spawnVMHost(name string) (pid int, err error) {
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("vz: resolving rask's own executable path: %w", err)
	}

	logPath := filepath.Join(r.dataDir(name), "vm-host.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, fmt.Errorf("vz: creating %s: %w", filepath.Dir(logPath), err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("vz: opening %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(self, "__vm-host", "--home", r.homeDir, "--name", name)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("vz: starting vm-host process: %w", err)
	}

	// The child is now detached; releasing it here (rather than holding
	// onto *exec.Cmd and calling Wait) avoids leaving a zombie once this
	// short-lived CLI process exits, since nothing in this process will
	// ever call Wait on it.
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("vz: releasing vm-host process: %w", err)
	}

	return cmd.Process.Pid, nil
}

// waitForVMState polls for vmhost.go's state file to appear (written as
// soon as the vm-host process's network is up, well before the guest
// finishes booting) or for the vm-host process to have already exited
// (a fast, definite failure signal — no point polling further).
func (r *Runtime) waitForVMState(ctx context.Context, name string, pid int) (vmState, error) {
	path := vmStatePath(r.homeDir, name)

	for {
		if state, err := readVMState(path); err == nil {
			return state, nil
		}

		if !processAlive(pid) {
			return vmState{}, fmt.Errorf("vz: vm-host process exited before reporting its network state (see %s)", filepath.Join(r.dataDir(name), "vm-host.log"))
		}

		select {
		case <-ctx.Done():
			return vmState{}, fmt.Errorf("vz: waiting for vm-host to report its network state: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// fetchKubeconfig retrieves the guest-internal admin kubeconfig (server
// "https://127.0.0.1:6443") and rewrites its server URL to the forwarded
// host port before writing it to r.kubeconfigPath(name) — the apiserver's
// serving certificate already carries 127.0.0.1 as a SAN (see
// internal/bootstrap/pki.go), so the same cert validates against either
// address, only the port differs.
func (r *Runtime) fetchKubeconfig(ctx context.Context, client *agentClient, name string, hostPort int) error {
	data, err := client.Kubeconfig(ctx)
	if err != nil {
		return fmt.Errorf("vz: fetching kubeconfig: %w", err)
	}

	cfg, err := clientcmd.Load(data)
	if err != nil {
		return fmt.Errorf("vz: parsing fetched kubeconfig: %w", err)
	}

	rewriteServerPort(cfg, hostPort)

	if err := clientcmd.WriteToFile(*cfg, r.kubeconfigPath(name)); err != nil {
		return fmt.Errorf("vz: writing kubeconfig: %w", err)
	}

	return nil
}

// rewriteServerPort replaces every cluster entry's server URL host:port
// with 127.0.0.1:hostPort, preserving scheme and path.
func rewriteServerPort(cfg *clientcmdapi.Config, hostPort int) {
	for _, c := range cfg.Clusters {
		c.Server = fmt.Sprintf("https://127.0.0.1:%d", hostPort)
	}
}

func (r *Runtime) fetchTimeline(ctx context.Context, client *agentClient, name string) error {
	data, err := client.Timeline(ctx)
	if err != nil {
		return fmt.Errorf("vz: fetching timeline: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(r.timelinePath(name)), 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", filepath.Dir(r.timelinePath(name)), err)
	}

	if err := os.WriteFile(r.timelinePath(name), data, 0o644); err != nil {
		return fmt.Errorf("vz: writing timeline: %w", err)
	}

	return nil
}

// Stop terminates the cluster's vm-host process (SIGTERM, which unblocks
// RunVMHost's ctx.Done() case and tears the VM down cleanly; SIGKILL if it
// doesn't exit within a grace period) and removes the PID/state files
// Delete's "still running" check and Start's next run key off of. A no-op
// if the cluster isn't running (mirrors
// internal/substrate/hostproc.Stop's idempotency contract).
func (r *Runtime) Stop(ctx context.Context, name string) error {
	pid, ok := r.readPID(name)
	if !ok {
		return nil
	}

	terminateVMHost(ctx, pid, vmHostGracePeriod)

	_ = os.Remove(r.pidPath(name))
	_ = os.Remove(vmStatePath(r.homeDir, name))

	return nil
}

// Delete removes a cluster instance and all of its state. Errors if the
// cluster is still running (its pidfile is present), matching
// substrate.Runtime's documented contract.
func (r *Runtime) Delete(ctx context.Context, name string) error {
	if _, ok := r.readPID(name); ok {
		return fmt.Errorf("vz: cluster %q is still running (call Stop first)", name)
	}

	return os.RemoveAll(cluster.Dir(r.homeDir, name))
}

// readPID reads and validates the cluster's pidfile. pid <= 0 is rejected
// (not just non-numeric content): every caller ultimately feeds this into
// syscall.Kill, and POSIX kill(2) treats pid -1 as "every process the
// caller may signal" and pid 0 as "every process in the caller's process
// group" — a corrupt or truncated pidfile must never be able to turn a
// single-cluster Stop/Delete into a broadcast signal to unrelated
// processes.
func (r *Runtime) readPID(name string) (int, bool) {
	data, err := os.ReadFile(r.pidPath(name))
	if err != nil {
		return 0, false
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		return 0, false
	}

	return pid, true
}

// processAlive reports whether pid identifies a live process, via the
// standard kill(pid, 0) liveness check (sends no signal, only validates
// the pid exists and is signalable by this process).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// Exec runs command with args inside the cluster's guest via rask-init's
// control agent, streaming its combined stdout/stderr and returning its
// exit code.
func (r *Runtime) Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (int, error) {
	client, err := r.agentClientFor(name)
	if err != nil {
		return 0, err
	}

	return client.Exec(ctx, stdout, command, args)
}

// WriteFile writes data to path inside the cluster's guest via rask-init's
// control agent.
func (r *Runtime) WriteFile(ctx context.Context, name string, path string, data []byte) error {
	client, err := r.agentClientFor(name)
	if err != nil {
		return err
	}

	return client.WriteFile(ctx, path, data)
}

// PortForward is not yet implemented for the vz substrate.
//
// gvisor-tap-vsock's Forwards map (network.go) is fixed at
// virtualnetwork.New time, before the guest even exists, to exactly two
// entries: the apiserver port (already covered by Start's kubeconfig
// rewriting) and the guest-agent port (covered by Exec/WriteFile). Forwarding
// an arbitrary third guest address would need either a dynamic forwarder
// (gvisor-tap-vsock has no public API for adding one after New) or dialing
// through the guest-agent's own network namespace, which is more machinery
// than v1 needs: no cmd/rask command calls PortForward today (see
// substrate.Runtime's doc comment), so this returns a clear error instead
// of a half-working implementation.
func (r *Runtime) PortForward(_ context.Context, name string, _, _ string) (<-chan error, error) {
	if _, ok := r.readPID(name); !ok {
		return nil, fmt.Errorf("vz: cluster %q is not running", name)
	}

	return nil, errors.New("vz: PortForward is not implemented yet for the vz substrate; see PortForward's doc comment")
}

func (r *Runtime) agentClientFor(name string) (*agentClient, error) {
	state, err := readVMState(vmStatePath(r.homeDir, name))
	if err != nil {
		return nil, fmt.Errorf("vz: cluster %q is not running: %w", name, err)
	}

	return newAgentClient(state.AgentHostPort), nil
}
