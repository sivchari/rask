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
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/substrate"
	"github.com/sivchari/rask/internal/substrate/vz/embedded"
)

// bootTimeout bounds how long Start waits, once vm-host is spawned, for it
// to report its network state and then for the guest agent to report
// healthy (node Ready + CoreDNS/local-path-provisioner applied — see
// cmd/rask-init/boot.go) before giving up.
//
// Deliberately does NOT also have to cover the one-time cost of building
// the template initramfs: that's Create's job, which pkg/cluster.Provider
// always runs to completion before Start (see Runtime.Create's doc
// comment), bounded separately and generously by
// initramfs.go's templateInitramfsBuildTimeout — a slow/cold download
// there now fails with its own clear error instead of silently eating
// into this budget.
//
// Found live during this investigation: with both waits sharing one
// 5-minute budget derived from whatever ctx the caller supplied, a caller
// that itself wraps a whole "create a cluster" call in a single outer
// timeout (a natural pattern — pkg/cluster.Provider.Create runs Create
// then Start against the very same ctx) could see that budget mostly
// consumed by Create's cold-cache downloads before Start even began,
// leaving this wait almost none of it — producing exactly this error,
// with no vm-host or guest bug at all, and (worse) Start's own failure
// path then force-terminating a vm-host process that might still have
// been legitimately working. What's actually left for this wait to cover,
// assuming Create already warmed the cache (vm-host's own redundant
// buildTemplateInitramfs call — see RunVMHost — then hits a cache hit) is
// vm-host's local prep (lock, memory check, disk/identifier/network setup)
// plus the guest's own boot: measured well under 10s end to end. Generous
// headroom over that for slower hardware or a first-ever guest kernel
// module load.
const bootTimeout = 90 * time.Second

// Runtime implements substrate.Runtime using a Virtualization.framework VM
// per cluster.
type Runtime struct {
	homeDir  string
	raskInit []byte
}

var _ substrate.Runtime = (*Runtime)(nil)

// New returns a Runtime for the macOS vz substrate, storing every
// cluster's state under homeDir (see internal/cluster.Dir). raskInit, if
// non-nil, is a pkg/cluster consumer's cluster.WithRaskInit injection — the
// linux/arm64 cmd/rask-init binary bytes Create/Start should embed into
// every cluster's initramfs instead of relying on this build's own embedded
// binary (see syncRaskInitOverride). nil for the rask CLI itself, which
// always cross-compiles a real one at build time.
func New(homeDir string, raskInit []byte) *Runtime {
	return &Runtime{homeDir: homeDir, raskInit: raskInit}
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
// If opts.ComponentDir is set, it is validated the same way
// internal/substrate/hostproc.Create validates it (via
// components.LocalDirSource) — the shared template initramfs itself is
// never rebuilt for it (still always the default binaries); Start instead
// layers a small per-cluster overlay onto it at boot (see
// componentoverride.go).
//
// Concurrently with both of the above, every image the cluster's default
// manifest bundle needs (or opts.CoreDNSImage, if set) is prefetched into a
// host-wide cache — see imageprefetch.go — mirroring
// internal/substrate/hostproc.Create's identical prefetch. Pure
// best-effort, like hostproc's: a failure here never fails Create.
func (r *Runtime) Create(ctx context.Context, name string, opts substrate.StartOptions) error {
	// Checked here, before any of Create's own (potentially slow: cold
	// downloads/builds) prep work, not only in Start: pkg/cluster.Provider
	// always calls Create before Start, so checking only in Start would
	// let a cold-cache Create run to completion first — many seconds to
	// minutes — before ever reporting an entitlement problem that was
	// knowable up front. See checkVirtualizationEntitlement's doc comment
	// for what this actually catches.
	if err := checkVirtualizationEntitlement(); err != nil {
		return err
	}

	cache := components.DefaultCache(filepath.Join(r.homeDir, "cache"))

	raskInitOverride, err := r.syncRaskInitOverride(cache)
	if err != nil {
		return err
	}

	imagesDone := make(chan struct{})

	go func() {
		defer close(imagesDone)
		prefetchImages(ctx, r.homeDir, opts.CoreDNSImage)
	}()

	_, tmplErr := buildTemplateInitramfs(ctx, cache, raskInitOverride)

	var componentErr error

	if opts.ComponentDir != "" {
		_, componentErr = components.NewLocalDirSource(opts.ComponentDir, cache).Resolve(ctx, components.DefaultK8sVersion, components.ARM64)
	}

	<-imagesDone

	if tmplErr != nil {
		return fmt.Errorf("vz: preparing template initramfs: %w", tmplErr)
	}

	if componentErr != nil {
		return fmt.Errorf("vz: validating --component-dir: %w", componentErr)
	}

	return nil
}

// syncRaskInitOverride keeps embedded.OverridePath(cache.Dir()) in sync
// with r.raskInit and returns that same path either way, so both this
// process's own buildTemplateInitramfs call (Create, above) and the
// separate "rask __vm-host" process's redundant one (vmhost.go's
// RunVMHost) can pass it to embedded.Resolve unconditionally: Resolve
// treats a missing file at that path as "no override", so neither caller
// needs to branch on whether r.raskInit was actually set.
//
//   - r.raskInit set (a cluster.WithRaskInit injection): validated with
//     embedded.ValidateRaskInit and written to the override path. Invalid
//     bytes fail here, immediately, rather than degrading into an opaque
//     boot timeout once RunVMHost eventually tries and fails to boot a
//     guest with them.
//   - r.raskInit nil: any override file left behind by an earlier
//     Provider construction against this same homeDir is removed, so a
//     plain (non-WithRaskInit) Runtime never silently inherits a previous
//     run's injected rask-init — homeDir's cache directory persists across
//     Provider instances (e.g. a library caller today, the rask CLI
//     tomorrow, against the same ~/.rask).
func (r *Runtime) syncRaskInitOverride(cache *components.Cache) (string, error) {
	path := embedded.OverridePath(cache.Dir())

	if r.raskInit == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("vz: removing stale rask-init override %s: %w", path, err)
		}

		return path, nil
	}

	if err := embedded.ValidateRaskInit(r.raskInit); err != nil {
		return "", fmt.Errorf("vz: invalid rask-init supplied via cluster.WithRaskInit: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("vz: creating %s: %w", filepath.Dir(path), err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, r.raskInit, 0o644); err != nil {
		return "", fmt.Errorf("vz: writing %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("vz: finalizing %s: %w", path, err)
	}

	return path, nil
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
// Extra API audiences and prebaked-seed selection are still not honored:
// vz's guest-side boot path does not yet plumb Config.ExtraAPIAudiences or
// Config.SeedPath through to the in-guest bootstrap.Boot call, and
// internal/prebake's seed-build path (which extracts a seed's source file
// from a stopped cluster's host filesystem) has no vz equivalent yet
// either, since a vz cluster's datastore lives inside the guest VM's own
// disk, not on a host-readable path. Those two are silently ignored, an
// existing, documented gap.
//
// opts.ExtraAPIServerArgs, opts.CoreDNSImage and opts.ComponentDir ARE now
// supported:
//
//   - ExtraAPIServerArgs and CoreDNSImage are staged host-side as a small
//     JSON internal/guestconfig.Config (stageClusterConfig, clusterconfig.go)
//     and read by rask-init at boot from guestlayout.ClusterConfigPath —
//     the guest has no shared filesystem with the host to pass flags any
//     other way (see the package doc comment).
//   - ComponentDir's five override binaries are staged host-side
//     (stageComponentOverride, componentoverride.go) and layered onto the
//     shared template initramfs as a second cpio archive at boot
//     (buildComponentOverlayCpio), rather than rebuilding the (per-host,
//     not per-cluster) template itself — see that function's doc comment
//     for why a later cpio entry at the same path safely overwrites the
//     template's own copy.
//
// All three are staged here (this process) and consumed by RunVMHost (a
// separate process spawnVMHost execs below) exactly like PrebootFiles
// already were, for the same reason: RunVMHost only shares dataDir with
// this invocation, not memory.
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
	// Also checked here, not only in Create (which pkg/cluster.Provider
	// always calls before this): Start is part of substrate.Runtime's
	// public contract on its own and callers other than Provider are free
	// to call it without Create — e.g. a future Stop-then-Start restart
	// path never goes through Create again. See checkVirtualizationEntitlement's
	// doc comment for why this matters at all, and RunVMHost's own copy of
	// this same check (defense in depth for "rask __vm-host" invoked
	// directly rather than through here).
	if entErr := checkVirtualizationEntitlement(); entErr != nil {
		return entErr
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

	// Same reasoning as StagePrebootFiles above: both must be on disk
	// before spawnVMHost's RunVMHost process starts building the combined
	// initramfs.
	if err = stageClusterConfig(r.dataDir(name), opts); err != nil {
		return fmt.Errorf("vz: %w", err)
	}

	if opts.ComponentDir != "" {
		cache := components.DefaultCache(filepath.Join(r.homeDir, "cache"))

		paths, resolveErr := components.NewLocalDirSource(opts.ComponentDir, cache).Resolve(ctx, components.DefaultK8sVersion, components.ARM64)
		if resolveErr != nil {
			return fmt.Errorf("vz: resolving --component-dir: %w", resolveErr)
		}

		if err = stageComponentOverride(r.dataDir(name), paths); err != nil {
			return fmt.Errorf("vz: %w", err)
		}
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

	// Capture the pid before anything below that could invalidate
	// cmd.Process.Pid: os.Process.Release's doc comment says outright that,
	// "for historical reasons, on systems other than Windows, Release sets
	// the Pid field to -1" — reading cmd.Process.Pid after Release()
	// therefore always returns -1, which every caller of spawnVMHost writes
	// straight into the cluster's pidfile. readPID's pid<=0 guard silently
	// swallows that instead of erroring, so Stop/Delete believe there is
	// nothing to terminate and no-op successfully while the real vm-host
	// process (and its VM) keeps running, unowned. Found live during a
	// previous session: every vm-host process that session had a "-1" (or
	// otherwise wrong) pidfile from the moment it started, and every prior
	// "successful" rask delete on a vz cluster was actually a no-op that
	// happened to look fine only because the orphaned process was
	// separately killed by hand each time.
	pid = cmd.Process.Pid

	// The child is now detached (Setsid, above); reap it asynchronously
	// rather than calling cmd.Process.Release(). Release() only stops this
	// package's own bookkeeping of the child — it does not wait(2) it, so
	// nothing reaps it once it exits. For the rask CLI's own short-lived
	// "rask create" invocation, that's harmless: the process exits moments
	// later anyway, and the kernel reparents (and macOS's launchd
	// promptly reaps) any remaining zombie child once its parent exits.
	// But a pkg/cluster.Provider caller is not guaranteed to be short-lived
	// — the whole point of the library is driving Create *and* a later
	// Stop/Delete of the very same cluster from one long-running process
	// (e.g. fjord). In that shape, this process stays alive across both
	// calls, so nothing ever reparents the zombie away: found live testing
	// exactly this — terminateVMHost's SIGKILL was delivered and the
	// process genuinely exited, but Stop still reported "did not exit even
	// after SIGKILL", because a zombie (exited, not yet reaped) still
	// answers kill(pid, 0) successfully — processAlive can't tell "still
	// running" from "exited but nobody called wait(2) on it" apart. This
	// goroutine is that wait(2): it simply blocks until the child actually
	// exits (whenever that is — this VM may run for hours) and discards
	// the result, which is exactly what turns a lingering zombie back into
	// "not alive" for processAlive's kill(pid, 0) checks to see correctly,
	// in both the short-lived-CLI and long-lived-library-caller cases
	// alike.
	go func() { _, _ = cmd.Process.Wait() }()

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

// PortForward forwards localAddr on the host to remoteAddr inside the
// cluster's guest VM, until ctx is canceled or the returned error channel is
// read from.
//
// Design: Runtime.PortForward is called from whatever CLI invocation asked
// for it ("rask port-forward" or a pkg/cluster caller), a different,
// possibly much later process than the "rask create" invocation that
// actually owns the running VM — Runtime.Start's own process has long since
// exited by the time this runs, exactly the constraint Stop/Delete already
// solve by reading state.go's persisted vmState instead of holding any
// in-memory reference. Three shapes were considered:
//
//   - Add a new control endpoint to the vm-host process (vmhost.go), which
//     already owns the gvisor-tap-vsock virtualnetwork.VirtualNetwork
//     (network.go) and could expose it for a later process to ask it to
//     open a forward. Rejected: that network's own stack has a single
//     static route, to its 192.168.127.0/24 tap subnet (see network.go's
//     createStack) — it can dial the guest's one NIC address
//     (192.168.127.2) but has no route to anything the guest's own kernel
//     routes internally (a pod IP via the CNI bridge, or a Service
//     ClusterIP via kube-proxy's iptables/ipvs rules), which live in a
//     different subnet entirely. Reaching those requires the dial to
//     happen from inside the guest's network namespace, not from
//     gvisor-tap-vsock's emulated one, so a vm-host-side endpoint doing
//     this dial itself wouldn't work regardless — see the next option.
//   - Dial directly from this process through gvisor-tap-vsock's own
//     host-side entry point. Rejected for the same routing reason: even
//     with a live *virtualnetwork.VirtualNetwork in hand, its stack still
//     can't reach anything past the guest's single NIC address.
//   - What this implements: rask-init's existing guest control agent
//     (internal/guestagent, already reachable cross-process through
//     vmState.AgentHostPort — the same forwarded port Exec/WriteFile
//     already use, no vm-host or state.go change needed at all) grows one
//     more endpoint, guestagent.PathTunnel, that dials remoteAddr from
//     inside the guest's own network namespace — the one vantage point
//     that actually has routes to pod/Service IPs — and relays raw bytes
//     over a hijacked connection (agentClient.Tunnel). This is the guest's
//     existing live RPC channel, not internal/guestconfig (also read
//     during this investigation): that package is a one-shot, boot-time
//     file staged into a cpio overlay and read once by rask-init before
//     bootstrap.Boot ever runs (see clusterconfig.go) — there is no
//     mechanism there for an on-demand, per-call request/response, so it
//     does not fit this use case, unlike guestagent's already-live HTTP
//     control surface.
//
// Lifetime: the host-side TCP listener this opens lives entirely in the
// calling process, not in vm-host — closing it (on ctx cancellation, or
// listener.Accept erroring) is enough to stop accepting new local
// connections; each already-accepted connection's own relay goroutines
// (relayThroughGuest) are independently unblocked by ctx the same way, so
// nothing here can leak a goroutine or a port in vm-host, since vm-host is
// never involved at all.
func (r *Runtime) PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (string, <-chan error, error) {
	client, err := r.agentClientFor(name)
	if err != nil {
		return "", nil, err
	}

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return "", nil, fmt.Errorf("vz: listening on %s: %w", localAddr, err)
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

				errCh <- fmt.Errorf("vz: accepting connection on %s: %w", localAddr, err)

				return
			}

			wg.Add(1)

			go func() {
				defer wg.Done()
				relayThroughGuest(ctx, client, conn, remoteAddr)
			}()
		}
	}()

	return listener.Addr().String(), errCh, nil
}

// relayThroughGuest opens a tunnel to remoteAddr through the guest control
// agent (agentClient.Tunnel) and copies bytes bidirectionally between it and
// conn until either side closes or ctx is done. conn is always closed before
// returning; a failed Tunnel call (e.g. the guest couldn't dial remoteAddr)
// simply closes conn with nothing relayed, mirroring
// internal/substrate/hostproc's relay, which does the same for a failed
// direct dial.
func relayThroughGuest(ctx context.Context, client *agentClient, conn net.Conn, remoteAddr string) {
	defer func() { _ = conn.Close() }()

	upstream, err := client.Tunnel(ctx, remoteAddr)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	// io.Copy below only returns once a Read/Write errors; it does not
	// itself observe ctx. Closing both connections when ctx is done is
	// what actually unblocks them on cancellation — same reasoning as
	// internal/substrate/hostproc's relay.
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

func (r *Runtime) agentClientFor(name string) (*agentClient, error) {
	state, err := readVMState(vmStatePath(r.homeDir, name))
	if err != nil {
		return nil, fmt.Errorf("vz: cluster %q is not running: %w", name, err)
	}

	return newAgentClient(state.AgentHostPort), nil
}
