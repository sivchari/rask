// Command spike-s1 proves (or refutes) rask's core bet: launching stock
// Kubernetes components directly (no kubeadm, no static pods) on top of
// kine/SQLite reaches "node Ready" in under 5 seconds, and measures the
// phase-by-phase latency breakdown. Must run as root inside a Linux VM.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type options struct {
	datadir    string
	workdir    string
	seedPath   string
	saveSeed   string
	keep       bool
	runs       int
	withPod    bool
	runTimeout time.Duration
}

func main() {
	var opts options
	flag.StringVar(&opts.datadir, "datadir", "/var/lib/rask-spike-s1", "state directory for the cluster under test (must be on a native VM filesystem, not virtiofs)")
	flag.StringVar(&opts.workdir, "workdir", "work", "directory containing downloaded component binaries (see fetch.sh)")
	flag.StringVar(&opts.seedPath, "seed", "", "path to a pre-populated kine sqlite db; if set, copied into place before starting kine")
	flag.StringVar(&opts.saveSeed, "save-seed", "", "after node Ready, cleanly stop kine and copy its db to this path (writes only, does not affect measured phases)")
	flag.BoolVar(&opts.keep, "keep", false, "skip teardown after the run (implies runs=1)")
	flag.IntVar(&opts.runs, "runs", 1, "number of measurement runs, each with full teardown+wipe between")
	flag.BoolVar(&opts.withPod, "with-pod", false, "stretch: apply a pause-image smoke pod after node Ready and measure t_pod_running")
	flag.DurationVar(&opts.runTimeout, "run-timeout", 60*time.Second, "max time to wait for node Ready (and pod Running) per run")
	flag.Parse()

	if err := realMain(opts); err != nil {
		log.Fatalf("spike-s1: %v", err)
	}
}

func realMain(opts options) error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root (sudo) — kubelet and containerd require it")
	}
	if opts.keep && opts.runs > 1 {
		return errors.New("--keep with --runs > 1 is not supported: teardown between runs is mandatory for repeated measurement")
	}

	workdir, err := filepath.Abs(opts.workdir)
	if err != nil {
		return fmt.Errorf("resolving workdir: %w", err)
	}
	bins, err := resolveBinaries(workdir)
	if err != nil {
		return err
	}

	datadir, err := filepath.Abs(opts.datadir)
	if err != nil {
		return fmt.Errorf("resolving datadir: %w", err)
	}

	nodeIP, err := detectOutboundIP()
	if err != nil {
		return err
	}
	fmt.Printf("node IP: %s\n", nodeIP)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	results := make([]*runResult, 0, opts.runs)
	for i := 0; i < opts.runs; i++ {
		fmt.Printf("\n=== run %d/%d ===\n", i+1, opts.runs)
		res := doRun(ctx, runConfig{
			options: opts,
			datadir: datadir,
			nodeIP:  nodeIP,
			bins:    bins,
		})
		if res.err != nil {
			fmt.Printf("run %d/%d FAILED: %v\n", i+1, opts.runs, res.err)
		} else {
			res.timeline.writeBreakdown(os.Stdout)
			if total, ok := res.timeline.total(); ok {
				fmt.Printf("TOTAL (t0 -> node_ready): %s\n", total.Round(time.Millisecond))
			}
		}
		results = append(results, res)
	}

	if opts.runs > 1 {
		printSummary(os.Stdout, results)
	}

	if failed := countFailed(results); failed > 0 {
		return fmt.Errorf("%d/%d runs failed", failed, opts.runs)
	}
	return nil
}

func countFailed(results []*runResult) int {
	n := 0
	for _, r := range results {
		if r.err != nil {
			n++
		}
	}
	return n
}

// binaries is the set of absolute paths to downloaded component binaries
// resolved once at startup.
type binaries struct {
	kineBin       string
	apiserverBin  string
	cmBin         string
	schedulerBin  string
	kubeletBin    string
	containerdBin string
	runcBin       string
	cniBinDir     string
}

func resolveBinaries(workdir string) (*binaries, error) {
	b := &binaries{
		kineBin:       filepath.Join(workdir, "kine"),
		apiserverBin:  filepath.Join(workdir, "kube-apiserver"),
		cmBin:         filepath.Join(workdir, "kube-controller-manager"),
		schedulerBin:  filepath.Join(workdir, "kube-scheduler"),
		kubeletBin:    filepath.Join(workdir, "kubelet"),
		containerdBin: filepath.Join(workdir, "containerd-bin", "bin", "containerd"),
		runcBin:       filepath.Join(workdir, "runc"),
		cniBinDir:     filepath.Join(workdir, "cni-bin"),
	}
	for _, p := range []string{b.kineBin, b.apiserverBin, b.cmBin, b.schedulerBin, b.kubeletBin, b.containerdBin, b.runcBin} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("missing binary %s (did you run fetch.sh?): %w", p, err)
		}
	}
	if _, err := os.Stat(b.cniBinDir); err != nil {
		return nil, fmt.Errorf("missing CNI plugin dir %s: %w", b.cniBinDir, err)
	}
	return b, nil
}

type runConfig struct {
	options
	datadir string
	nodeIP  string
	bins    *binaries
}

type runResult struct {
	timeline *timeline
	err      error
}

// doRun executes a single measurement: wipe -> generate PKI/config -> boot
// the cluster in parallel phases -> wait for node Ready -> optional pod
// smoke test -> optional seed save -> teardown.
func doRun(ctx context.Context, cfg runConfig) *runResult {
	if err := os.RemoveAll(cfg.datadir); err != nil {
		return &runResult{err: fmt.Errorf("wiping datadir: %w", err)}
	}
	if err := os.MkdirAll(cfg.datadir, 0o755); err != nil {
		return &runResult{err: fmt.Errorf("creating datadir: %w", err)}
	}

	logDir := filepath.Join(cfg.datadir, "logs")

	var procs procList
	defer teardown(&procs, cfg.datadir, cfg.keep)

	if cfg.seedPath != "" {
		if err := seedKineDB(cfg.seedPath, filepath.Join(cfg.datadir, "kine")); err != nil {
			return &runResult{err: fmt.Errorf("seeding kine db: %w", err)}
		}
	}

	p, err := buildPKI(cfg.datadir, cfg.nodeIP)
	if err != nil {
		return &runResult{err: err}
	}
	if err := writeCNIConfig(filepath.Join(cfg.datadir, "cni", "net.d")); err != nil {
		return &runResult{err: err}
	}
	cpaths, err := writeContainerdConfig(cfg.datadir, cfg.bins.runcBin, cfg.bins.cniBinDir)
	if err != nil {
		return &runResult{err: err}
	}
	kpaths, err := writeKubeletConfig(cfg.datadir, filepath.Join(cfg.datadir, "pki", "ca.crt"), cpaths.socketPath)
	if err != nil {
		return &runResult{err: err}
	}

	runCtx, cancel := context.WithTimeout(ctx, cfg.runTimeout)
	defer cancel()

	t0 := time.Now()
	tl := newTimeline(t0)

	if err := bootCluster(runCtx, cfg, tl, &procs, logDir, p, cpaths, kpaths); err != nil {
		return &runResult{timeline: tl, err: err}
	}

	if cfg.saveSeed != "" {
		if err := saveSeedDB(&procs, filepath.Join(cfg.datadir, "kine"), cfg.saveSeed); err != nil {
			fmt.Printf("warning: --save-seed failed: %v\n", err)
		} else {
			fmt.Printf("seed saved to %s\n", cfg.saveSeed)
		}
	}

	return &runResult{timeline: tl}
}

// bootCluster runs the parallel component startup graph:
//
//	kine -> apiserver ---+--> controller-manager + scheduler
//	                     |
//	containerd ----------+--> kubelet --> node registered --> node Ready [--> pod Running]
func bootCluster(ctx context.Context, cfg runConfig, tl *timeline, procs *procList, logDir string, p *pki, cpaths *containerdPaths, kpaths *kubeletPaths) error {
	apiserverReady := make(chan struct{})
	containerdReady := make(chan struct{})

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(p.caPEM) {
		return errors.New("failed to parse CA cert into pool")
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		proc, err := startProc("containerd", cfg.bins.containerdBin, []string{"--config", cpaths.configPath}, logDir)
		if err != nil {
			return err
		}
		procs.add(proc)
		if err := waitUnixSocket(gctx, cpaths.socketPath); err != nil {
			return fmt.Errorf("containerd did not become ready: %w", err)
		}
		tl.mark("containerd_up")
		close(containerdReady)
		return nil
	})

	g.Go(func() error {
		kineSocket := filepath.Join(cfg.datadir, "kine", "kine.sock")
		kineDB := filepath.Join(cfg.datadir, "kine", "state.db")
		if err := os.MkdirAll(filepath.Dir(kineSocket), 0o755); err != nil {
			return fmt.Errorf("creating kine dir: %w", err)
		}
		kineProc, err := startProc("kine", cfg.bins.kineBin, []string{
			"--listen-address", "unix://" + kineSocket,
			"--endpoint", "sqlite://" + kineDB + "?_journal=WAL&cache=shared",
			"--metrics-bind-address", "0",
		}, logDir)
		if err != nil {
			return err
		}
		procs.add(kineProc)
		if err := waitUnixSocket(gctx, kineSocket); err != nil {
			return fmt.Errorf("kine did not become ready: %w", err)
		}
		tl.mark("kine_up")

		apiserverArgs := []string{
			"--etcd-servers=unix://" + kineSocket,
			"--service-account-issuer=https://kubernetes.default.svc.cluster.local",
			"--service-account-signing-key-file=" + filepath.Join(cfg.datadir, "pki", "sa.key"),
			"--service-account-key-file=" + filepath.Join(cfg.datadir, "pki", "sa.pub"),
			"--api-audiences=https://kubernetes.default.svc.cluster.local",
			"--authorization-mode=Node,RBAC",
			"--service-cluster-ip-range=" + serviceClusterIPRange,
			"--anonymous-auth=false",
			"--profiling=false",
			"--allow-privileged=true",
			"--client-ca-file=" + filepath.Join(cfg.datadir, "pki", "ca.crt"),
			"--tls-cert-file=" + filepath.Join(cfg.datadir, "pki", "apiserver.crt"),
			"--tls-private-key-file=" + filepath.Join(cfg.datadir, "pki", "apiserver.key"),
			"--secure-port=" + strconv.Itoa(apiserverPort),
			"--advertise-address=" + cfg.nodeIP,
		}
		apiserverProc, err := startProc("kube-apiserver", cfg.bins.apiserverBin, apiserverArgs, logDir)
		if err != nil {
			return err
		}
		procs.add(apiserverProc)

		// anonymous-auth is off, so even /readyz needs a credential:
		// authentication happens before the "always allow" authorization
		// exemption for health endpoints. Use the admin client cert.
		adminCert, err := tls.X509KeyPair(p.adminCertPEM, p.adminKeyPEM)
		if err != nil {
			return fmt.Errorf("loading admin client cert: %w", err)
		}
		apiserverClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{adminCert}}}}
		if err := waitHTTPOK(gctx, apiserverClient, fmt.Sprintf("https://127.0.0.1:%d/readyz", apiserverPort)); err != nil {
			return fmt.Errorf("apiserver did not become ready: %w", err)
		}
		tl.mark("apiserver_readyz")
		close(apiserverReady)

		return startControlPlane(gctx, cfg, tl, procs, logDir, p)
	})

	g.Go(func() error {
		if err := waitClosed(gctx, apiserverReady); err != nil {
			return err
		}
		if err := waitClosed(gctx, containerdReady); err != nil {
			return err
		}

		kubeletArgs := []string{
			"--config=" + kpaths.configPath,
			"--kubeconfig=" + p.kubeletKubeconfig,
			"--root-dir=" + kpaths.rootDir,
			"--cert-dir=" + kpaths.certDir,
			"--hostname-override=" + nodeName,
			"--node-ip=" + cfg.nodeIP,
		}
		kubeletProc, err := startProc("kubelet", cfg.bins.kubeletBin, kubeletArgs, logDir)
		if err != nil {
			return err
		}
		procs.add(kubeletProc)

		kubeletClient := &http.Client{}
		if err := waitHTTPOK(gctx, kubeletClient, fmt.Sprintf("http://127.0.0.1:%d/healthz", kubeletHealthzPort)); err != nil {
			return fmt.Errorf("kubelet did not become ready: %w", err)
		}
		tl.mark("kubelet_started")
		return nil
	})

	g.Go(func() error {
		if err := waitClosed(gctx, apiserverReady); err != nil {
			return err
		}
		clientset, err := buildClientset(p.adminKubeconfig)
		if err != nil {
			return fmt.Errorf("building clientset: %w", err)
		}
		if err := watchNodeReady(gctx, clientset, tl); err != nil {
			return fmt.Errorf("waiting for node Ready: %w", err)
		}

		if cfg.withPod {
			if err := applySmokePod(gctx, clientset, tl); err != nil {
				// Stretch goal: log but don't fail the core measurement.
				fmt.Printf("warning: smoke pod did not reach Running: %v\n", err)
			}
		}
		return nil
	})

	return g.Wait()
}

// startControlPlane launches kube-controller-manager and kube-scheduler in
// parallel, marking cm_sched_started once both have passed their healthz.
// Their serving certs are self-signed by each component (no --tls-cert-file
// given), so the health probe intentionally does not verify server
// identity — this is a loopback health check, not a security boundary.
func startControlPlane(ctx context.Context, cfg runConfig, tl *timeline, procs *procList, logDir string, p *pki) error {
	cg, cgctx := errgroup.WithContext(ctx)
	insecureClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	cg.Go(func() error {
		proc, err := startProc("kube-controller-manager", cfg.bins.cmBin, []string{
			"--kubeconfig=" + p.cmKubeconfig,
			"--leader-elect=false",
			// Required so node-lifecycle-controller (taint removal on
			// Ready) runs as its own system:controller:node-controller
			// service account rather than the coarser
			// system:kube-controller-manager identity, which the built-in
			// RBAC bootstrap policy does not grant node write access to.
			"--use-service-account-credentials=true",
			"--service-account-private-key-file=" + filepath.Join(cfg.datadir, "pki", "sa.key"),
			"--root-ca-file=" + filepath.Join(cfg.datadir, "pki", "ca.crt"),
			"--cluster-signing-cert-file=" + filepath.Join(cfg.datadir, "pki", "ca.crt"),
			"--cluster-signing-key-file=" + filepath.Join(cfg.datadir, "pki", "ca.key"),
		}, logDir)
		if err != nil {
			return err
		}
		procs.add(proc)
		return waitHTTPOK(cgctx, insecureClient, "https://127.0.0.1:10257/healthz")
	})

	cg.Go(func() error {
		proc, err := startProc("kube-scheduler", cfg.bins.schedulerBin, []string{
			"--kubeconfig=" + p.schedKubeconfig,
			"--leader-elect=false",
		}, logDir)
		if err != nil {
			return err
		}
		procs.add(proc)
		return waitHTTPOK(cgctx, insecureClient, "https://127.0.0.1:10259/healthz")
	})

	if err := cg.Wait(); err != nil {
		return fmt.Errorf("controller-manager/scheduler did not become ready: %w", err)
	}
	tl.mark("cm_sched_started")
	return nil
}

// waitClosed blocks until ch is closed or ctx is done.
func waitClosed(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func buildClientset(kubeconfigPath string) (*kubernetes.Clientset, error) {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}

// watchNodeReady watches for nodeName to appear (marks node_registered) and
// then for its Ready condition to become True (marks node_ready).
func watchNodeReady(ctx context.Context, clientset *kubernetes.Clientset, tl *timeline) error {
	registered := false
	for {
		w, err := clientset.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("metadata.name", nodeName).String(),
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
				continue
			}
		}

		ready, err := drainNodeWatch(ctx, w, tl, &registered)
		w.Stop()
		if ready {
			return nil
		}
		if err != nil {
			return err
		}
		// Channel closed without reaching Ready (e.g. apiserver hiccup); retry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func drainNodeWatch(ctx context.Context, w watch.Interface, tl *timeline, registered *bool) (ready bool, err error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				return false, nil
			}
			node, ok := event.Object.(*corev1.Node)
			if !ok {
				continue
			}
			if !*registered {
				tl.mark("node_registered")
				*registered = true
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					tl.mark("node_ready")
					return true, nil
				}
			}
		}
	}
}

// applySmokePod creates a pause-image pod (retrying until the "default"
// namespace exists, which apiserver's bootstrap-controller creates
// asynchronously) and waits for it to reach Running.
func applySmokePod(ctx context.Context, clientset *kubernetes.Clientset, tl *timeline) error {
	falseVal := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: "default"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &falseVal,
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: sandboxImage,
			}},
		},
	}

	for {
		_, err := clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("creating smoke pod: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}

	for {
		w, err := clientset.CoreV1().Pods("default").Watch(ctx, metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("metadata.name", "smoke").String(),
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
				continue
			}
		}
		running, err := drainPodWatch(ctx, w, tl)
		w.Stop()
		if running {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func drainPodWatch(ctx context.Context, w watch.Interface, tl *timeline) (running bool, err error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				return false, nil
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning {
				tl.mark("pod_running")
				return true, nil
			}
		}
	}
}

// procList is a concurrency-safe collection of managed child processes.
type procList struct {
	mu    sync.Mutex
	procs []*managedProc
}

func (pl *procList) add(p *managedProc) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.procs = append(pl.procs, p)
}

func (pl *procList) snapshot() []*managedProc {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]*managedProc, len(pl.procs))
	copy(out, pl.procs)
	return out
}

// teardown kills every tracked process (whole process group, to catch
// containerd shims), waits for exit, unmounts anything left under datadir
// by containerd's snapshotter, and wipes datadir unless keep is set.
func teardown(pl *procList, datadir string, keep bool) {
	procs := pl.snapshot()
	for i := len(procs) - 1; i >= 0; i-- {
		procs[i].stop(syscall.SIGTERM)
	}
	time.Sleep(300 * time.Millisecond)
	for i := len(procs) - 1; i >= 0; i-- {
		procs[i].stop(syscall.SIGKILL)
	}

	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *managedProc) {
			defer wg.Done()
			p.wait()
		}(p)
	}
	wg.Wait()

	unmountUnder(datadir)

	if !keep {
		_ = os.RemoveAll(datadir)
	}

	// Best-effort: the CNI bridge plugin creates a host-side "cni0" bridge
	// on first pod sandbox; remove it so repeated runs don't inherit stale
	// bridge/iptables state.
	_ = exec.Command("ip", "link", "delete", "cni0").Run()

	// A pod sandbox killed out from under containerd (rather than stopped
	// via CNI DEL) leaves its network namespace behind; clean those up too
	// so repeated --with-pod runs don't accumulate orphaned netns/veths.
	cleanupLeftoverPodNetns()
}

func cleanupLeftoverPodNetns() {
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.Fields(line)
		if len(name) == 0 || !strings.HasPrefix(name[0], "cni-") {
			continue
		}
		_ = exec.Command("ip", "netns", "delete", name[0]).Run()
	}
}

// seedKineDB copies a previously-saved kine sqlite db into dstDir before
// kine starts, measuring the "prebaked seed" boot path.
func seedKineDB(seedPath, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return copyFile(seedPath, filepath.Join(dstDir, "state.db"))
}

// saveSeedDB gracefully stops the kine process (SIGTERM, so sqlite
// checkpoints its WAL into the main db file cleanly) and copies the
// resulting db out to dst.
func saveSeedDB(pl *procList, kineDir, dst string) error {
	var kineProc *managedProc
	for _, p := range pl.snapshot() {
		if p.name == "kine" {
			kineProc = p
		}
	}
	if kineProc == nil {
		return errors.New("kine process not found")
	}
	kineProc.stop(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { kineProc.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		kineProc.stop(syscall.SIGKILL)
		<-done
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(filepath.Join(kineDir, "state.db"), dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return nil
}

func printSummary(w io.Writer, results []*runResult) {
	totals := make([]time.Duration, 0, len(results))
	phaseSamples := make(map[string][]time.Duration)

	for _, r := range results {
		if r.err != nil || r.timeline == nil {
			continue
		}
		if total, ok := r.timeline.total(); ok {
			totals = append(totals, total)
		}
		for _, name := range orderedNames {
			if d, ok := r.timeline.elapsed(name); ok {
				phaseSamples[name] = append(phaseSamples[name], d)
			}
		}
	}

	fmt.Fprintf(w, "\n=== summary (%d/%d runs succeeded) ===\n", len(totals), len(results))
	fmt.Fprintf(w, "%-20s %10s %10s %10s\n", "PHASE (cumulative)", "p50", "p95", "n")
	for _, name := range orderedNames {
		samples := phaseSamples[name]
		if len(samples) == 0 {
			continue
		}
		fmt.Fprintf(w, "%-20s %10s %10s %10d\n", name, percentile(samples, 50).Round(time.Millisecond), percentile(samples, 95).Round(time.Millisecond), len(samples))
	}
	if len(totals) > 0 {
		fmt.Fprintf(w, "%-20s %10s %10s %10d\n", "TOTAL", percentile(totals, 50).Round(time.Millisecond), percentile(totals, 95).Round(time.Millisecond), len(totals))
	}
}

func percentile(durs []time.Duration, p float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
