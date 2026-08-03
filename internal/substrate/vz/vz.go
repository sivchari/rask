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

// diskPath must match vmhost.go's RunVMHost, which is the only writer of
// this file — kept as a literal "disk.img" join here (not a shared
// constant) only because vmhost.go builds it from its own local dataDir
// variable rather than calling a Runtime method.
func (r *Runtime) diskPath(name string) string {
	return filepath.Join(r.dataDir(name), "disk.img")
}

// Create prepares (but does not start) a cluster instance: ensures the
// template initramfs and guest kernel are cached (the first call on a
// fresh host pays this download/build cost; later calls are instant), and
// creates the per-cluster virtio-blk data disk file. Does not touch
// cluster.Dir(homeDir, name) itself for the same reason
// internal/substrate/hostproc.Create doesn't: a failed Create should leave
// no trace that would block a retried "rask create".
//
// opts.ComponentDir is rejected with a clear error if set — see
// Start's doc comment for why a --component-dir override has no vz
// equivalent yet.
func (r *Runtime) Create(ctx context.Context, name string, opts substrate.StartOptions) error {
	if embedded.IsPlaceholder() {
		return errors.New("vz: internal/substrate/vz/embedded/rask-init is still the placeholder: run `make build-rask-init` first")
	}

	if opts.ComponentDir != "" {
		return errors.New("vz: --component-dir is not supported by the vz substrate yet (see Start's doc comment); the shared template initramfs every vz cluster boots from is built once per host, not per cluster, so a per-cluster component override needs a per-cluster initramfs — not yet implemented")
	}

	cache := components.DefaultCache(filepath.Join(r.homeDir, "cache"))

	if _, err := buildTemplateInitramfs(ctx, cache); err != nil {
		return fmt.Errorf("vz: preparing template initramfs: %w", err)
	}

	return nil
}

// Start first fails fast (see the peekVMLock check below) if a VM is
// already running elsewhere on this host — only one may run at a time
// (lock.go) — then spawns the cluster's VM as a detached "rask __vm-host"
// child process (see package doc), waits for the guest to report healthy
// through its control agent, then persists the PID, admin kubeconfig
// (rewritten to point at the forwarded host port) and boot timeline for
// later Stop/Delete/--verbose calls.
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
//
// Most of opts is not honored yet: extra API audiences and prebaked-seed
// selection are threaded through the hostproc substrate (see
// internal/substrate/hostproc.Runtime.Start); vz's guest-side boot path does
// not yet plumb Config.ExtraAPIAudiences or Config.SeedPath through to the
// in-guest bootstrap.Boot call, and internal/prebake's seed-build path
// (which extracts a seed's source file from a stopped cluster's host
// filesystem) has no vz equivalent yet either, since a vz cluster's
// datastore lives inside the guest VM's own disk, not on a host-readable
// path. Those two are silently ignored, an existing, documented gap.
//
// opts.ExtraAPIServerArgs and opts.CoreDNSImage are new fields with no vz
// support at all yet and are rejected with a clear error instead of being
// silently dropped like the two above: unlike a missing TokenReview
// audience or a slower cold boot (SeedPath), silently ignoring either of
// these would substitute a caller-specified security-relevant apiserver
// flag or container image with rask's own default without any visible
// signal that happened.
//
// opts.PrebootFiles IS supported: they are staged host-side under
// r.dataDir(name) (substrate.StagePrebootFiles) and RunVMHost — which,
// unlike this function, does run on the same host filesystem as that
// staging directory — reads them back and injects them into the guest via
// a per-cluster cpio archive (see preboot.go's buildPrebootCpio) concatenated
// onto the shared template initramfs, since the guest VM itself has no
// shared filesystem with the host to read StartOptions.PrebootFiles.Src
// paths from directly.
func (r *Runtime) Start(ctx context.Context, name string, opts substrate.StartOptions) (err error) {
	if len(opts.ExtraAPIServerArgs) > 0 {
		return errors.New("vz: --apiserver-arg is not supported by the vz substrate yet")
	}

	if opts.CoreDNSImage != "" {
		return errors.New("vz: --coredns-image is not supported by the vz substrate yet")
	}

	// Fail fast, before creating any state or spawning a vm-host process
	// at all, if a VM is already running elsewhere on this host: without
	// this, a doomed Start only discovered the conflict once
	// waitForVMState's process-liveness check or bootTimeout fired,
	// minutes later, via a generic error that never named the actual
	// holder — found live during this session (host-wide flock in
	// vm-host.go's own RunVMHost still fails fast internally, but nothing
	// short-circuited *this* process's wait for it to do so). See
	// peekVMLock's doc comment for why this check is racy-but-fine.
	if holder, busy, err := peekVMLock(r.homeDir); err != nil {
		return fmt.Errorf("vz: checking host VM lock: %w", err)
	} else if busy {
		return lockConflictError(holder)
	}

	clusterDir := cluster.Dir(r.homeDir, name)
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", clusterDir, err)
	}

	var pid int

	defer func() {
		if err != nil {
			if pid > 0 {
				if termErr := terminateVMHost(context.Background(), pid, vmHostGracePeriod); termErr != nil {
					// Best-effort cleanup during an already-failing
					// Start: nothing left to do but make the failure
					// visible rather than silently leaving the
					// vm-host process (and its VM) running unowned.
					fmt.Fprintf(os.Stderr, "vz: cleaning up after failed Start: %v\n", termErr)
				}
			}

			_ = os.RemoveAll(clusterDir)
		}
	}()

	// Staged host-side, read back and injected into the guest's per-cluster
	// cpio by RunVMHost (a separate process spawnVMHost execs below) — see
	// preboot.go's buildPrebootCpio. Must happen before spawnVMHost: by the
	// time that process starts building the combined initramfs, every
	// preboot file needs to already be on disk.
	if err = substrate.StagePrebootFiles(r.dataDir(name), opts.PrebootFiles); err != nil {
		return fmt.Errorf("vz: %w", err)
	}

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
// "rask __vm-host --name <name>", detached into a brand new session
// (Setsid) so it survives this CLI invocation exiting, any terminal signal
// (e.g. Ctrl-C) delivered to this invocation's process group while Start
// is still waiting for boot to finish, and — the actual reason this is
// Setsid and not just Setpgid — any SIGHUP the controlling
// terminal/session generates for the rest of vm-host's life, not only at
// spawn time.
//
// Found live during this session: a healthy vm-host process died silently
// ~100s into a cluster's life, with zero trace in vm-host.log (no panic,
// no watchdog message, no vz state-change error — see vmhost.go's
// handleVMStateChange for the other half of this fix), correlated with an
// unrelated sibling background process (a kubectl port-forward) being
// killed by an external harness. Setpgid alone only leaves the process
// *group*, not the *session* — a vm-host started that way is still a
// member of whatever session spawned "rask create", so it stays reachable
// by that session's own signal delivery (e.g. a controlling terminal
// hanging up) for its entire life. Go's default disposition for an
// unhandled SIGHUP is to terminate the process immediately, running no
// deferred cleanup (stopVM/console.Close/net.Close/lock.Release never
// fire) and logging nothing — indistinguishable from this incident.
// Setsid closes that off entirely; vmhost_darwin.go additionally now
// catches SIGHUP explicitly as defense in depth, so even a directly
// targeted "kill -HUP" (which Setsid alone cannot stop, since it is not a
// broadcast) becomes a clean, logged shutdown instead of a silent one.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("vz: starting vm-host process: %w", err)
	}

	// Capture the pid before Release(): os.Process.Release's doc comment
	// says outright that, "for historical reasons, on systems other than
	// Windows, Release sets the Pid field to -1" — reading cmd.Process.Pid
	// after Release() therefore always returns -1, which every caller of
	// spawnVMHost writes straight into the cluster's pidfile. readPID's
	// pid<=0 guard silently swallows that instead of erroring, so Stop/
	// Delete believe there is nothing to terminate and no-op successfully
	// while the real vm-host process (and its VM) keeps running, unowned.
	// Found live during this session: every vm-host process this session
	// had a "-1" (or otherwise wrong) pidfile from the moment it started,
	// and every prior "successful" rask delete on a vz cluster was actually
	// a no-op that happened to look fine only because the orphaned process
	// was separately killed by hand each time.
	pid = cmd.Process.Pid

	// The child is now detached; releasing it here (rather than holding
	// onto *exec.Cmd and calling Wait) avoids leaving a zombie once this
	// short-lived CLI process exits, since nothing in this process will
	// ever call Wait on it.
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("vz: releasing vm-host process: %w", err)
	}

	return pid, nil
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
// doesn't exit within a grace period) and, only once that's actually
// confirmed, removes the PID/state files Delete's "still running" check
// and Start's next run key off of. A no-op if the cluster isn't running
// (mirrors internal/substrate/hostproc.Stop's idempotency contract).
//
// If the process cannot be confirmed dead, Stop returns an error and
// leaves the pidfile in place — it must NOT report success while the
// process (and the VM it owns) might still be running. A previous version
// removed the pidfile unconditionally regardless of whether termination
// actually succeeded, which let a subsequent Delete see "not running" and
// remove the cluster's state directory while the real vm-host process (and
// its VM) kept running, unowned — found live during this session.
func (r *Runtime) Stop(ctx context.Context, name string) error {
	pid, ok := r.readPID(name)
	if !ok {
		return nil
	}

	if err := terminateVMHost(ctx, pid, vmHostGracePeriod); err != nil {
		return fmt.Errorf("vz: stopping cluster %q: %w", name, err)
	}

	_ = os.Remove(r.pidPath(name))
	_ = os.Remove(vmStatePath(r.homeDir, name))

	return nil
}

// Delete removes a cluster instance and all of its state. Errors if the
// cluster is still running (its pidfile is present), matching
// substrate.Runtime's documented contract.
//
// Before removing anything, it also does a best-effort, warn-only check
// (warnLeakedXPCProcesses) for a Virtualization XPC process that still
// has this cluster's disk image open despite vm-host no longer running —
// see that function's doc comment for why this only ever warns and never
// kills.
func (r *Runtime) Delete(ctx context.Context, name string) error {
	if _, ok := r.readPID(name); ok {
		return fmt.Errorf("vz: cluster %q is still running (call Stop first)", name)
	}

	warnLeakedXPCProcesses(name, r.diskPath(name))

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
func (r *Runtime) PortForward(_ context.Context, name string, _, _ string) (string, <-chan error, error) {
	if _, ok := r.readPID(name); !ok {
		return "", nil, fmt.Errorf("vz: cluster %q is not running", name)
	}

	return "", nil, errors.New("vz: PortForward is not implemented yet for the vz substrate; see PortForward's doc comment")
}

func (r *Runtime) agentClientFor(name string) (*agentClient, error) {
	state, err := readVMState(vmStatePath(r.homeDir, name))
	if err != nil {
		return nil, fmt.Errorf("vz: cluster %q is not running: %w", name, err)
	}

	return newAgentClient(state.AgentHostPort), nil
}
