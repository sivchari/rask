package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/store"
)

// defaultClusterName is used when Config.ClusterName is empty.
const defaultClusterName = "rask"

// Bounded default deadlines for every readiness wait in the boot DAG.
// Without these, a component that starts but never becomes healthy (wrong
// cgroup driver, a missing device, a crash-looping binary — real operator
// errors, not hypotheticals: both found live during the M3 in-container
// trials) hangs "rask create" forever
// instead of failing fast with a clear, phase-named error. Values are
// generous relative to this project's own measured boot latencies
// (node_ready in ~3s on a bare-host Linux measurement) so a slow or
// contended machine doesn't trip a false timeout, while still bounding the
// wait. Each is threaded into its phase function as an explicit parameter
// (mirrors internal/substrate/vz/watchdog.go's runBootWatchdog, which takes
// its timeout the same way) so unit tests can pass a short duration
// directly instead of waiting out the real default.
const (
	datastoreReadyTimeout    = 30 * time.Second
	containerdReadyTimeout   = 30 * time.Second
	apiserverReadyTimeout    = 60 * time.Second
	controlPlaneReadyTimeout = 30 * time.Second
	kubeletReadyTimeout      = 60 * time.Second
	kubeProxyReadyTimeout    = 30 * time.Second
	nodeReadyTimeout         = 60 * time.Second
)

// Config is everything Boot needs to launch one cluster instance's control
// plane and node.
type Config struct {
	// ClusterName becomes the cluster/context name in every generated
	// kubeconfig. Defaults to defaultClusterName if empty.
	ClusterName string

	// DataDir is the native-filesystem root this cluster's state lives
	// under (datastore, containerd root/state, kubelet root, PKI,
	// kubeconfigs, CNI config). Must be a native fs path, not a
	// virtiofs/9p share: containerd's overlayfs snapshotter needs real
	// Linux filesystem semantics.
	DataDir string

	// NodeIP is the address the API server advertises and kubelet binds
	// to.
	NodeIP string

	// Paths are the resolved component binary paths (see
	// internal/components.Cache.Ensure).
	Paths *components.Paths

	// Datastore is the (not-yet-started) backing store for the API
	// server. Injected rather than constructed here so Boot stays
	// substrate-agnostic and unit-testable with a fake in place of a
	// real kine process.
	Datastore store.Datastore

	// SeedPath, if set, is a prebaked datastore snapshot applied via
	// Datastore.SeedFrom before Start, skipping the cluster bootstrap
	// reconciliation that otherwise dominates apiserver_readyz latency.
	SeedPath string

	// LogDir is where every launched component's combined stdout/stderr
	// is captured. Defaults to DataDir/logs if empty.
	LogDir string

	// ExtraAPIAudiences are additional kube-apiserver --api-audiences
	// values beyond the cluster's own service-account issuer, e.g. for a
	// TokenReview client that requests a custom audience (see
	// apiAudiences in config.go).
	ExtraAPIAudiences []string

	// ExtraAPIServerArgs are additional "key=value" kube-apiserver flags
	// (kubeadm-style, no leading "--"), appended to the flags rask itself
	// sets below. A key naming one of rask's own flags is rejected with an
	// error (see buildAPIServerArgs in config.go for why, and for the
	// collision semantics).
	ExtraAPIServerArgs []string
}

// Result is what Boot returns once the node is Ready.
type Result struct {
	// Timeline is the phase-by-phase latency breakdown (see
	// timeline.go), useful for "rask create --verbose".
	Timeline *Timeline

	// Supervisor keeps every launched component running until
	// Supervisor.Stop is called.
	Supervisor *Supervisor

	// PKI is every credential and kubeconfig Boot generated.
	PKI *ClusterPKI

	// AdminKubeconfigPath is PKI.AdminKubeconfigPath, repeated here since
	// it's what most callers actually need.
	AdminKubeconfigPath string
}

// Boot brings up one rask cluster instance's control plane and node,
// blocking until the node is Ready, following the DAG:
//
//	datastore -> apiserver ---+--> controller-manager + scheduler
//	                          |--> kube-proxy
//	containerd ---------------+--> kubelet --> node registered --> node ready
//
// It returns once node Ready is observed. The returned Result.Supervisor
// (and Config.Datastore) keep running until the caller stops them; Boot
// itself does not tear anything down on success. On failure, everything
// Boot itself launched is stopped before the error is returned.
func Boot(ctx context.Context, cfg Config) (*Result, error) {
	t0 := time.Now()
	tl := NewTimeline(t0)

	clusterName := cfg.ClusterName
	if clusterName == "" {
		clusterName = defaultClusterName
	}

	logDir := cfg.LogDir
	if logDir == "" {
		logDir = filepath.Join(cfg.DataDir, "logs")
	}

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("bootstrap: creating log dir %s: %w", logDir, err)
	}

	cpki, err := generatePKI(cfg.DataDir, cfg.NodeIP, clusterName)
	if err != nil {
		return nil, err
	}

	cniConfDir := filepath.Join(cfg.DataDir, "cni", "net.d")
	if err := writeCNIConfig(cniConfDir); err != nil {
		return nil, err
	}

	cpaths, err := writeContainerdConfig(cfg.DataDir, cfg.Paths.Runc, cfg.Paths.CNIBinDir, cniConfDir)
	if err != nil {
		return nil, err
	}

	kpaths, err := writeKubeletConfig(cfg.DataDir, cpki.CACertPath, cpaths.socketPath)
	if err != nil {
		return nil, err
	}

	kubeProxyConfigPath, err := writeKubeProxyConfig(cfg.DataDir, cpki.KubeProxyKubeconfigPath)
	if err != nil {
		return nil, err
	}

	if cfg.SeedPath != "" {
		if err := cfg.Datastore.SeedFrom(ctx, cfg.SeedPath); err != nil {
			return nil, err
		}
	}

	sup := NewSupervisor()

	if err := runBootDAG(ctx, cfg, tl, sup, cpki, cpaths, kpaths, kubeProxyConfigPath, logDir); err != nil {
		sup.Stop()
		// cfg.Datastore runs outside Supervisor (it manages its own
		// process lifecycle — see internal/store/kine), so a DAG
		// failure after Datastore.Start succeeded would otherwise
		// leak it as an orphaned process with nothing left to track
		// or stop it.
		_ = cfg.Datastore.Stop(context.Background())

		return nil, err
	}

	return &Result{
		Timeline:            tl,
		Supervisor:          sup,
		PKI:                 cpki,
		AdminKubeconfigPath: cpki.AdminKubeconfigPath,
	}, nil
}

// runBootDAG runs the parallel component startup graph described on Boot's
// doc comment, marking tl at each phase transition, and returns once the
// node is Ready.
//
// Every long-running process is launched with launchCtx (Boot's original,
// stable ctx), never with an errgroup-derived context: golang.org/x/sync/errgroup's
// WithContext explicitly cancels its derived context "the first time Wait
// returns, whichever occurs first" — including a *successful* return, i.e.
// the exact moment this whole DAG finishes because the node became Ready.
// Since Supervisor.Launch ties each process's lifetime to the context it's
// given (via exec.CommandContext), launching with that derived context
// would SIGKILL every component (apiserver included) the instant boot
// succeeds — caught by an actual E2E run against real binaries, not by any
// unit test, since boot_test.go's fake-datastore test only exercised the
// failure path (where that cancellation is correct). waitCtx (the
// errgroup-derived one) is still used for readiness polling, so one
// branch's failure still fails the others fast.
func runBootDAG(launchCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpki *ClusterPKI, cpaths *containerdPaths, kpaths *kubeletPaths, kubeProxyConfigPath, logDir string) error {
	apiserverReady := make(chan struct{})
	containerdReady := make(chan struct{})

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(cpki.CA.CertPEM) {
		return errors.New("bootstrap: failed to parse generated CA cert into pool")
	}

	g, waitCtx := errgroup.WithContext(launchCtx)

	g.Go(func() error {
		return bootContainerd(launchCtx, waitCtx, cfg, tl, sup, cpaths, containerdReady, logDir, containerdReadyTimeout)
	})

	g.Go(func() error {
		return bootDatastoreAndControlPlane(launchCtx, waitCtx, cfg, tl, sup, cpki, caPool, apiserverReady, logDir)
	})

	g.Go(func() error {
		if err := waitClosed(waitCtx, apiserverReady); err != nil {
			return err
		}

		if err := waitClosed(waitCtx, containerdReady); err != nil {
			return err
		}

		return bootKubelet(launchCtx, waitCtx, cfg, tl, sup, kpaths, cpki, logDir, kubeletReadyTimeout)
	})

	g.Go(func() error {
		if err := waitClosed(waitCtx, apiserverReady); err != nil {
			return err
		}

		return bootKubeProxy(launchCtx, waitCtx, cfg, tl, sup, kubeProxyConfigPath, logDir, kubeProxyReadyTimeout)
	})

	g.Go(func() error {
		if err := waitClosed(waitCtx, apiserverReady); err != nil {
			return err
		}

		clientset, err := buildClientset(cpki.AdminKubeconfigPath)
		if err != nil {
			return fmt.Errorf("bootstrap: building clientset: %w", err)
		}

		if err := watchNodeReady(waitCtx, clientset, tl, nodeReadyTimeout); err != nil {
			return fmt.Errorf("bootstrap: node did not become ready within %s: %w", nodeReadyTimeout, err)
		}

		return nil
	})

	return g.Wait()
}

// bootContainerd launches containerd and waits for its unix socket to
// accept connections, bounded by timeout so a containerd that starts but
// never opens its socket doesn't hang the DAG forever. See runBootDAG's
// doc comment for why launchCtx and waitCtx are different contexts.
func bootContainerd(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpaths *containerdPaths, containerdReady chan<- struct{}, logDir string, timeout time.Duration) error {
	spec := ProcessSpec{Name: "containerd", Path: cfg.Paths.Containerd, Args: []string{"--config", cpaths.configPath}, LogPath: filepath.Join(logDir, "containerd.log")}
	if err := sup.Launch(launchCtx, spec); err != nil {
		return err
	}

	readyCtx, cancel := context.WithTimeout(waitCtx, timeout)
	defer cancel()

	if err := waitUnixSocket(readyCtx, cpaths.socketPath); err != nil {
		return fmt.Errorf("bootstrap: containerd did not become ready within %s: %w", timeout, err)
	}

	tl.Mark("containerd_up")
	close(containerdReady)

	return nil
}

// bootDatastoreAndControlPlane starts the datastore, then the API server
// once the datastore reports ready, then controller-manager and scheduler
// once the API server reports ready. See runBootDAG's doc comment for why
// launchCtx and waitCtx are different contexts.
func bootDatastoreAndControlPlane(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpki *ClusterPKI, caPool *x509.CertPool, apiserverReady chan<- struct{}, logDir string) error {
	dsCtx, dsCancel := context.WithTimeout(waitCtx, datastoreReadyTimeout)
	defer dsCancel()

	endpoint, err := cfg.Datastore.Start(dsCtx)
	if err != nil {
		return fmt.Errorf("bootstrap: starting datastore within %s: %w", datastoreReadyTimeout, err)
	}

	tl.Mark("kine_up")

	issuer := "https://kubernetes.default.svc." + cluster.Domain

	apiserverArgs := []string{
		"--etcd-servers=" + endpoint,
		"--service-account-issuer=" + issuer,
		"--service-account-signing-key-file=" + cpki.ServiceAccountPrivPath,
		"--service-account-key-file=" + cpki.ServiceAccountPubPath,
		"--api-audiences=" + apiAudiences(issuer, cfg.ExtraAPIAudiences),
		"--authorization-mode=Node,RBAC",
		"--service-cluster-ip-range=" + cluster.ServiceCIDR,
		"--anonymous-auth=false",
		"--profiling=false",
		"--allow-privileged=true",
		"--client-ca-file=" + cpki.CACertPath,
		"--tls-cert-file=" + cpki.APIServerCertPath,
		"--tls-private-key-file=" + cpki.APIServerKeyPath,
		// Credential apiserver presents outbound to kubelet's
		// exec/logs/port-forward streaming server. Without these, that
		// server rejects apiserver with "Unauthorized" (kubelet's own
		// authentication.x509.clientCAFile requires a client cert;
		// anonymous auth is off) — see pki.go's issuance site for why
		// this cert carries no Organization.
		"--kubelet-client-certificate=" + cpki.KubeletClientCertPath,
		"--kubelet-client-key=" + cpki.KubeletClientKeyPath,
		// apiserver's default preference order tries the Node's Hostname
		// address (cluster.NodeName, "rask-node") first, which has no DNS
		// record anywhere rask runs — every exec/logs/port-forward request
		// would fail node name resolution before it even reached the
		// kubelet-client-certificate credential above. InternalIP (the
		// same cfg.NodeIP kubelet was started with, via --node-ip) is
		// always dialable; kind sets the same InternalIP-first order for
		// the identical reason.
		"--kubelet-preferred-address-types=InternalIP,Hostname",
		"--secure-port=" + strconv.Itoa(apiserverPort),
		"--advertise-address=" + cfg.NodeIP,
	}

	apiserverArgs, err = buildAPIServerArgs(apiserverArgs, cfg.ExtraAPIServerArgs)
	if err != nil {
		return err
	}

	apiserverSpec := ProcessSpec{Name: "kube-apiserver", Path: cfg.Paths.KubeAPIServer, Args: apiserverArgs, LogPath: filepath.Join(logDir, "kube-apiserver.log")}
	if err := sup.Launch(launchCtx, apiserverSpec); err != nil {
		return err
	}

	// anonymous-auth is off, so even /readyz needs a credential:
	// authentication happens before the "always allow" authorization
	// exemption for health endpoints (a gotcha found during the M0 s1 spike).
	adminTLSCert, err := tls.X509KeyPair(cpki.AdminCert.CertPEM, cpki.AdminCert.KeyPEM)
	if err != nil {
		return fmt.Errorf("bootstrap: loading admin client cert: %w", err)
	}

	apiserverClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{adminTLSCert}}}}

	readyzURL := fmt.Sprintf("https://127.0.0.1:%d/readyz", apiserverPort)

	apiCtx, apiCancel := context.WithTimeout(waitCtx, apiserverReadyTimeout)
	defer apiCancel()

	if err := waitHTTPOK(apiCtx, apiserverClient, readyzURL); err != nil {
		return fmt.Errorf("bootstrap: apiserver did not become ready within %s: %w", apiserverReadyTimeout, err)
	}

	tl.Mark("apiserver_readyz")
	close(apiserverReady)

	return bootControlPlane(launchCtx, waitCtx, cfg, tl, sup, cpki, caPool, logDir, controlPlaneReadyTimeout)
}

// bootControlPlane launches kube-controller-manager and kube-scheduler in
// parallel, marking cm_sched_started once both pass their healthz probes.
// Each is given its own loopback serving certificate signed by the
// cluster's CA (issueLoopbackServingCert in pki.go), so the health probe
// verifies server identity via caPool like every other component's
// readiness check, rather than skipping TLS verification. See runBootDAG's
// doc comment for why launchCtx and waitCtx are different contexts.
func bootControlPlane(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpki *ClusterPKI, caPool *x509.CertPool, logDir string, timeout time.Duration) error {
	healthClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool}}}

	cg, cgWaitCtx := errgroup.WithContext(waitCtx)

	cg.Go(func() error {
		spec := ProcessSpec{Name: "kube-controller-manager", Path: cfg.Paths.KubeControllerManager, LogPath: filepath.Join(logDir, "kube-controller-manager.log"), Args: []string{
			"--kubeconfig=" + cpki.ControllerManagerKubeconfigPath,
			"--leader-elect=false",
			"--tls-cert-file=" + cpki.ControllerManagerCertPath,
			"--tls-private-key-file=" + cpki.ControllerManagerKeyPath,
			// Required so node-lifecycle-controller (taint removal on
			// Ready) runs as its own system:controller:node-controller
			// service account rather than the coarser
			// system:kube-controller-manager identity, which the
			// built-in RBAC bootstrap policy does not grant node write
			// access to (a gotcha found during the M0 s1 spike).
			"--use-service-account-credentials=true",
			"--service-account-private-key-file=" + cpki.ServiceAccountPrivPath,
			"--root-ca-file=" + cpki.CACertPath,
			"--cluster-signing-cert-file=" + cpki.CACertPath,
			"--cluster-signing-key-file=" + cpki.CAKeyPath,
		}}
		if err := sup.Launch(launchCtx, spec); err != nil {
			return err
		}

		readyCtx, cancel := context.WithTimeout(cgWaitCtx, timeout)
		defer cancel()

		if err := waitHTTPOK(readyCtx, healthClient, "https://127.0.0.1:10257/healthz"); err != nil {
			return fmt.Errorf("kube-controller-manager did not become ready within %s: %w", timeout, err)
		}

		return nil
	})

	cg.Go(func() error {
		spec := ProcessSpec{Name: "kube-scheduler", Path: cfg.Paths.KubeScheduler, LogPath: filepath.Join(logDir, "kube-scheduler.log"), Args: []string{
			"--kubeconfig=" + cpki.SchedulerKubeconfigPath,
			"--leader-elect=false",
			"--tls-cert-file=" + cpki.SchedulerCertPath,
			"--tls-private-key-file=" + cpki.SchedulerKeyPath,
		}}
		if err := sup.Launch(launchCtx, spec); err != nil {
			return err
		}

		readyCtx, cancel := context.WithTimeout(cgWaitCtx, timeout)
		defer cancel()

		if err := waitHTTPOK(readyCtx, healthClient, "https://127.0.0.1:10259/healthz"); err != nil {
			return fmt.Errorf("kube-scheduler did not become ready within %s: %w", timeout, err)
		}

		return nil
	})

	if err := cg.Wait(); err != nil {
		return fmt.Errorf("bootstrap: controller-manager/scheduler did not become ready: %w", err)
	}

	tl.Mark("cm_sched_started")

	return nil
}

// bootKubelet launches kubelet once both the API server and containerd are
// ready, and waits for its healthz probe. See runBootDAG's doc comment for
// why launchCtx and waitCtx are different contexts.
func bootKubelet(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, kpaths *kubeletPaths, cpki *ClusterPKI, logDir string, timeout time.Duration) error {
	spec := ProcessSpec{Name: "kubelet", Path: cfg.Paths.Kubelet, LogPath: filepath.Join(logDir, "kubelet.log"), Args: []string{
		"--config=" + kpaths.configPath,
		"--kubeconfig=" + cpki.KubeletKubeconfigPath,
		"--root-dir=" + kpaths.rootDir,
		"--cert-dir=" + kpaths.certDir,
		"--hostname-override=" + cluster.NodeName,
		"--node-ip=" + cfg.NodeIP,
	}}
	if err := sup.Launch(launchCtx, spec); err != nil {
		return err
	}

	readyCtx, cancel := context.WithTimeout(waitCtx, timeout)
	defer cancel()

	if err := waitHTTPOK(readyCtx, http.DefaultClient, "http://127.0.0.1:10248/healthz"); err != nil {
		return fmt.Errorf("bootstrap: kubelet did not become ready within %s: %w", timeout, err)
	}

	tl.Mark("kubelet_started")

	return nil
}

// bootKubeProxy launches kube-proxy once the API server is ready. See
// runBootDAG's doc comment for why launchCtx and waitCtx are different
// contexts.
//
// kube-proxy runs as a supervised host process here, not a DaemonSet pod:
// rask's v1 hostproc substrate is single-node with no per-cluster
// networking namespace or DaemonSet scheduling story yet (see
// internal/substrate/hostproc's package doc), so a DaemonSet buys no
// isolation and only adds an image pull plus a pod-scheduling round trip to
// the critical boot path. The rendered KubeProxyConfiguration
// (writeKubeProxyConfig) uses the same shape a DaemonSet's ConfigMap would
// mount, so this stays portable if rask grows a DaemonSet form later.
func bootKubeProxy(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, kubeProxyConfigPath, logDir string, timeout time.Duration) error {
	spec := ProcessSpec{Name: "kube-proxy", Path: cfg.Paths.KubeProxy, LogPath: filepath.Join(logDir, "kube-proxy.log"), Args: []string{
		"--config=" + kubeProxyConfigPath,
		"--hostname-override=" + cluster.NodeName,
	}}
	if err := sup.Launch(launchCtx, spec); err != nil {
		return err
	}

	readyCtx, cancel := context.WithTimeout(waitCtx, timeout)
	defer cancel()

	if err := waitHTTPOK(readyCtx, http.DefaultClient, "http://127.0.0.1:10256/healthz"); err != nil {
		return fmt.Errorf("bootstrap: kube-proxy did not become ready within %s: %w", timeout, err)
	}

	tl.Mark("kube_proxy_started")

	return nil
}

func buildClientset(kubeconfigPath string) (*kubernetes.Clientset, error) {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(restConfig)
}

// watchNodeReady watches for cluster.NodeName to register (marking
// node_registered) and then for its Ready condition to become True
// (marking node_ready), via a real client-go watch rather than polling,
// bounded by timeout so a node that never registers or never reaches
// Ready doesn't hang the DAG forever. clientset takes the kubernetes.Interface
// clientset methods actually need (rather than the concrete
// *kubernetes.Clientset buildClientset returns), so this is unit-testable
// against client-go's fake clientset — mirrors
// internal/manifests.WaitDeploymentReady's existing precedent.
func watchNodeReady(ctx context.Context, clientset kubernetes.Interface, tl *Timeline, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	registered := false

	for {
		w, err := clientset.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("metadata.name", cluster.NodeName).String(),
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

func drainNodeWatch(ctx context.Context, w watch.Interface, tl *Timeline, registered *bool) (ready bool, err error) {
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
				tl.Mark("node_registered")
				*registered = true
			}

			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					tl.Mark("node_ready")

					return true, nil
				}
			}
		}
	}
}
