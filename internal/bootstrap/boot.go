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
		spec := ProcessSpec{Name: "containerd", Path: cfg.Paths.Containerd, Args: []string{"--config", cpaths.configPath}, LogPath: filepath.Join(logDir, "containerd.log")}
		if err := sup.Launch(launchCtx, spec); err != nil {
			return err
		}

		if err := waitUnixSocket(waitCtx, cpaths.socketPath); err != nil {
			return fmt.Errorf("bootstrap: containerd did not become ready: %w", err)
		}

		tl.Mark("containerd_up")
		close(containerdReady)

		return nil
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

		return bootKubelet(launchCtx, waitCtx, cfg, tl, sup, kpaths, cpki, logDir)
	})

	g.Go(func() error {
		if err := waitClosed(waitCtx, apiserverReady); err != nil {
			return err
		}

		return bootKubeProxy(launchCtx, waitCtx, cfg, tl, sup, kubeProxyConfigPath, logDir)
	})

	g.Go(func() error {
		if err := waitClosed(waitCtx, apiserverReady); err != nil {
			return err
		}

		clientset, err := buildClientset(cpki.AdminKubeconfigPath)
		if err != nil {
			return fmt.Errorf("bootstrap: building clientset: %w", err)
		}

		return watchNodeReady(waitCtx, clientset, tl)
	})

	return g.Wait()
}

// bootDatastoreAndControlPlane starts the datastore, then the API server
// once the datastore reports ready, then controller-manager and scheduler
// once the API server reports ready. See runBootDAG's doc comment for why
// launchCtx and waitCtx are different contexts.
func bootDatastoreAndControlPlane(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpki *ClusterPKI, caPool *x509.CertPool, apiserverReady chan<- struct{}, logDir string) error {
	endpoint, err := cfg.Datastore.Start(waitCtx)
	if err != nil {
		return fmt.Errorf("bootstrap: starting datastore: %w", err)
	}

	tl.Mark("kine_up")

	apiserverArgs := []string{
		"--etcd-servers=" + endpoint,
		"--service-account-issuer=https://kubernetes.default.svc." + cluster.Domain,
		"--service-account-signing-key-file=" + cpki.ServiceAccountPrivPath,
		"--service-account-key-file=" + cpki.ServiceAccountPubPath,
		"--api-audiences=https://kubernetes.default.svc." + cluster.Domain,
		"--authorization-mode=Node,RBAC",
		"--service-cluster-ip-range=" + cluster.ServiceCIDR,
		"--anonymous-auth=false",
		"--profiling=false",
		"--allow-privileged=true",
		"--client-ca-file=" + cpki.CACertPath,
		"--tls-cert-file=" + cpki.APIServerCertPath,
		"--tls-private-key-file=" + cpki.APIServerKeyPath,
		"--secure-port=" + strconv.Itoa(apiserverPort),
		"--advertise-address=" + cfg.NodeIP,
	}

	apiserverSpec := ProcessSpec{Name: "kube-apiserver", Path: cfg.Paths.KubeAPIServer, Args: apiserverArgs, LogPath: filepath.Join(logDir, "kube-apiserver.log")}
	if err := sup.Launch(launchCtx, apiserverSpec); err != nil {
		return err
	}

	// anonymous-auth is off, so even /readyz needs a credential:
	// authentication happens before the "always allow" authorization
	// exemption for health endpoints (see gotcha in spikes/s1/RESULTS.md).
	adminTLSCert, err := tls.X509KeyPair(cpki.AdminCert.CertPEM, cpki.AdminCert.KeyPEM)
	if err != nil {
		return fmt.Errorf("bootstrap: loading admin client cert: %w", err)
	}

	apiserverClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{adminTLSCert}}}}

	readyzURL := fmt.Sprintf("https://127.0.0.1:%d/readyz", apiserverPort)
	if err := waitHTTPOK(waitCtx, apiserverClient, readyzURL); err != nil {
		return fmt.Errorf("bootstrap: apiserver did not become ready: %w", err)
	}

	tl.Mark("apiserver_readyz")
	close(apiserverReady)

	return bootControlPlane(launchCtx, waitCtx, cfg, tl, sup, cpki, caPool, logDir)
}

// bootControlPlane launches kube-controller-manager and kube-scheduler in
// parallel, marking cm_sched_started once both pass their healthz probes.
// Each is given its own loopback serving certificate signed by the
// cluster's CA (issueLoopbackServingCert in pki.go), so the health probe
// verifies server identity via caPool like every other component's
// readiness check, rather than skipping TLS verification. See runBootDAG's
// doc comment for why launchCtx and waitCtx are different contexts.
func bootControlPlane(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, cpki *ClusterPKI, caPool *x509.CertPool, logDir string) error {
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
			// access to (see gotcha in spikes/s1/RESULTS.md).
			"--use-service-account-credentials=true",
			"--service-account-private-key-file=" + cpki.ServiceAccountPrivPath,
			"--root-ca-file=" + cpki.CACertPath,
			"--cluster-signing-cert-file=" + cpki.CACertPath,
			"--cluster-signing-key-file=" + cpki.CAKeyPath,
		}}
		if err := sup.Launch(launchCtx, spec); err != nil {
			return err
		}

		return waitHTTPOK(cgWaitCtx, healthClient, "https://127.0.0.1:10257/healthz")
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

		return waitHTTPOK(cgWaitCtx, healthClient, "https://127.0.0.1:10259/healthz")
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
func bootKubelet(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, kpaths *kubeletPaths, cpki *ClusterPKI, logDir string) error {
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

	if err := waitHTTPOK(waitCtx, http.DefaultClient, "http://127.0.0.1:10248/healthz"); err != nil {
		return fmt.Errorf("bootstrap: kubelet did not become ready: %w", err)
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
func bootKubeProxy(launchCtx, waitCtx context.Context, cfg Config, tl *Timeline, sup *Supervisor, kubeProxyConfigPath, logDir string) error {
	spec := ProcessSpec{Name: "kube-proxy", Path: cfg.Paths.KubeProxy, LogPath: filepath.Join(logDir, "kube-proxy.log"), Args: []string{
		"--config=" + kubeProxyConfigPath,
		"--hostname-override=" + cluster.NodeName,
	}}
	if err := sup.Launch(launchCtx, spec); err != nil {
		return err
	}

	if err := waitHTTPOK(waitCtx, http.DefaultClient, "http://127.0.0.1:10256/healthz"); err != nil {
		return fmt.Errorf("bootstrap: kube-proxy did not become ready: %w", err)
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
// (marking node_ready), via a real client-go watch rather than polling.
func watchNodeReady(ctx context.Context, clientset *kubernetes.Clientset, tl *Timeline) error {
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
