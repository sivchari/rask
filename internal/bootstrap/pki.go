package bootstrap

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/pki"
)

// apiserverPort is the fixed port every rask cluster's API server listens
// on, matching cluster identity constants like cluster.NodeName in being
// held constant rather than randomized per create.
const apiserverPort = 6443

// ClusterPKI is every credential and kubeconfig rask's boot DAG needs for
// one cluster instance, generated fresh at create time and written to disk
// under dataDir.
type ClusterPKI struct {
	CA *pki.CA

	// AdminCert is retained (not just written to AdminKubeconfigPath) so
	// Boot can build an authenticated *tls.Config for polling apiserver's
	// /readyz directly: with --anonymous-auth=false, even the health
	// endpoint requires a client certificate.
	AdminCert *pki.Cert

	CACertPath                string
	CAKeyPath                 string
	APIServerCertPath         string
	APIServerKeyPath          string
	ServiceAccountPrivPath    string
	ServiceAccountPubPath     string
	ControllerManagerCertPath string
	ControllerManagerKeyPath  string
	SchedulerCertPath         string
	SchedulerKeyPath          string

	// KubeletClientCertPath and KubeletClientKeyPath are the API server's
	// own client credential for authenticating outbound to kubelet's
	// exec/logs/port-forward streaming endpoints (--kubelet-client-certificate
	// / --kubelet-client-key). See the issuance site below for why this
	// carries no Organization, unlike kubeadm's own convention.
	KubeletClientCertPath string
	KubeletClientKeyPath  string

	AdminKubeconfigPath             string
	KubeletKubeconfigPath           string
	ControllerManagerKubeconfigPath string
	SchedulerKubeconfigPath         string
	KubeProxyKubeconfigPath         string
}

// generatePKI creates a fresh CA and every leaf certificate and kubeconfig
// rask's control plane and node need, writing them under
// dataDir/{pki,kubeconfigs}. advertiseIP, if non-empty, is added as an
// extra IP SAN on the API server's serving certificate alongside the fixed
// loopback and cluster.APIServerServiceIP addresses. clusterName becomes
// every generated kubeconfig's cluster/context name, so a substrate
// implementation that copies AdminKubeconfigPath out for external use gets
// a context named after the actual cluster instance (what
// "rask export kubeconfig" and "rask delete" key off of), not a constant.
func generatePKI(dataDir, advertiseIP, clusterName string) (*ClusterPKI, error) {
	pkiDir := filepath.Join(dataDir, "pki")
	kcDir := filepath.Join(dataDir, "kubeconfigs")

	for _, dir := range []string{pkiDir, kcDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("bootstrap: creating %s: %w", dir, err)
		}
	}

	ca, err := pki.NewCA("rask-ca")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: generating CA: %w", err)
	}

	caCertPath := filepath.Join(pkiDir, "ca.crt")
	if err := writePEM(caCertPath, ca.CertPEM); err != nil {
		return nil, err
	}

	// kube-controller-manager's CSR-signing controllers need the CA's
	// private key on disk (--cluster-signing-key-file), not just its
	// certificate. Without this file, controller-manager fails to start
	// ANY controller at all (a single controller construction error
	// aborts its whole startup sequence) — including the deployment
	// controller, so Deployments silently never get a ReplicaSet. Found
	// via a real E2E run: /healthz still reported healthy despite this,
	// since it doesn't reflect controller-startup failures.
	caKeyPath := filepath.Join(pkiDir, "ca.key")
	if err := writePEM(caKeyPath, ca.KeyPEM); err != nil {
		return nil, err
	}

	apiSANs := []string{"kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc." + cluster.Domain, "localhost"}
	apiIPs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(cluster.APIServerServiceIP)}

	if advertiseIP != "" {
		if ip := net.ParseIP(advertiseIP); ip != nil {
			apiIPs = append(apiIPs, ip)
		}
	}

	apiCert, err := ca.IssueServer("kube-apiserver", apiSANs, apiIPs)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: issuing apiserver serving cert: %w", err)
	}

	apiCertPath := filepath.Join(pkiDir, "apiserver.crt")
	apiKeyPath := filepath.Join(pkiDir, "apiserver.key")

	if err := writePEM(apiCertPath, apiCert.CertPEM); err != nil {
		return nil, err
	}

	if err := writePEM(apiKeyPath, apiCert.KeyPEM); err != nil {
		return nil, err
	}

	saKeys, err := pki.NewServiceAccountKeyPair()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: generating service account keys: %w", err)
	}

	saPrivPath := filepath.Join(pkiDir, "sa.key")
	saPubPath := filepath.Join(pkiDir, "sa.pub")

	if err := writePEM(saPrivPath, saKeys.PrivatePEM); err != nil {
		return nil, err
	}

	if err := writePEM(saPubPath, saKeys.PublicPEM); err != nil {
		return nil, err
	}

	// Loopback serving certs for controller-manager and scheduler, so
	// their health probes can verify server identity instead of skipping
	// TLS verification: both bind only to 127.0.0.1 (never
	// --bind-address'd externally), but the health probe still trusts
	// the same CA as everything else rather than trusting nothing.
	cmServingCertPath, cmServingKeyPath, err := issueLoopbackServingCert(ca, pkiDir, "controller-manager", "kube-controller-manager")
	if err != nil {
		return nil, err
	}

	schedServingCertPath, schedServingKeyPath, err := issueLoopbackServingCert(ca, pkiDir, "scheduler", "kube-scheduler")
	if err != nil {
		return nil, err
	}

	// apiserver's outbound credential to kubelet's exec/logs/port-forward
	// streaming server (kubelet's authentication.x509.clientCAFile trusts
	// this same CA). Deliberately issued with no Organization, unlike
	// kubeadm's "O=system:masters" convention: this cert is only ever
	// presented outbound by apiserver to kubelet, never used to
	// authenticate an inbound request to apiserver itself, and kubelet's
	// authorization.mode here is AlwaysAllow (writeKubeletConfig in
	// config.go), which grants any authenticated identity regardless of
	// group. Giving it "system:masters" would add zero function today
	// while turning it into a second cluster-admin credential on disk
	// (the same CA authenticates it to apiserver too, if authorization
	// mode ever changed) for no benefit — narrower stays simple here, so
	// that's what's used; a group would only start mattering if kubelet's
	// authorization.mode ever moved to Webhook, at which point a
	// dedicated RBAC binding for whatever group this cert carries would
	// need to be added at the same time.
	kubeletClientCert, err := ca.IssueClient("kube-apiserver-kubelet-client", nil)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: issuing apiserver kubelet-client cert: %w", err)
	}

	kubeletClientCertPath := filepath.Join(pkiDir, "apiserver-kubelet-client.crt")
	kubeletClientKeyPath := filepath.Join(pkiDir, "apiserver-kubelet-client.key")

	if err := writePEM(kubeletClientCertPath, kubeletClientCert.CertPEM); err != nil {
		return nil, err
	}

	if err := writePEM(kubeletClientKeyPath, kubeletClientCert.KeyPEM); err != nil {
		return nil, err
	}

	apiserverURL := fmt.Sprintf("https://127.0.0.1:%d", apiserverPort)

	adminKubeconfigPath, adminCert, err := writeClientKubeconfig(ca, kcDir, apiserverURL, clusterName, "admin", "admin", []string{"system:masters"})
	if err != nil {
		return nil, err
	}

	kubeletKubeconfigPath, _, err := writeClientKubeconfig(ca, kcDir, apiserverURL, clusterName, "kubelet", "system:node:"+cluster.NodeName, []string{"system:nodes"})
	if err != nil {
		return nil, err
	}

	cmKubeconfigPath, _, err := writeClientKubeconfig(ca, kcDir, apiserverURL, clusterName, "controller-manager", "system:kube-controller-manager", nil)
	if err != nil {
		return nil, err
	}

	schedKubeconfigPath, _, err := writeClientKubeconfig(ca, kcDir, apiserverURL, clusterName, "scheduler", "system:kube-scheduler", nil)
	if err != nil {
		return nil, err
	}

	proxyKubeconfigPath, _, err := writeClientKubeconfig(ca, kcDir, apiserverURL, clusterName, "kube-proxy", "system:kube-proxy", nil)
	if err != nil {
		return nil, err
	}

	return &ClusterPKI{
		CA:                              ca,
		AdminCert:                       adminCert,
		CACertPath:                      caCertPath,
		CAKeyPath:                       caKeyPath,
		APIServerCertPath:               apiCertPath,
		APIServerKeyPath:                apiKeyPath,
		ServiceAccountPrivPath:          saPrivPath,
		ServiceAccountPubPath:           saPubPath,
		ControllerManagerCertPath:       cmServingCertPath,
		ControllerManagerKeyPath:        cmServingKeyPath,
		SchedulerCertPath:               schedServingCertPath,
		SchedulerKeyPath:                schedServingKeyPath,
		KubeletClientCertPath:           kubeletClientCertPath,
		KubeletClientKeyPath:            kubeletClientKeyPath,
		AdminKubeconfigPath:             adminKubeconfigPath,
		KubeletKubeconfigPath:           kubeletKubeconfigPath,
		ControllerManagerKubeconfigPath: cmKubeconfigPath,
		SchedulerKubeconfigPath:         schedKubeconfigPath,
		KubeProxyKubeconfigPath:         proxyKubeconfigPath,
	}, nil
}

// issueLoopbackServingCert issues a serving certificate valid for
// "localhost" and 127.0.0.1 (every rask control-plane component other than
// the API server binds only to loopback) and writes it to
// pkiDir/<fileStem>.{crt,key}.
func issueLoopbackServingCert(ca *pki.CA, pkiDir, fileStem, commonName string) (certPath, keyPath string, err error) {
	cert, err := ca.IssueServer(commonName, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		return "", "", fmt.Errorf("bootstrap: issuing %s serving cert: %w", commonName, err)
	}

	certPath = filepath.Join(pkiDir, fileStem+".crt")
	keyPath = filepath.Join(pkiDir, fileStem+".key")

	if err := writePEM(certPath, cert.CertPEM); err != nil {
		return "", "", err
	}

	if err := writePEM(keyPath, cert.KeyPEM); err != nil {
		return "", "", err
	}

	return certPath, keyPath, nil
}

// writeClientKubeconfig issues a client certificate (commonName, orgs)
// signed by ca and writes a kubeconfig for it at kcDir/<userName>.kubeconfig,
// with its cluster and context both named clusterName, returning both the
// kubeconfig path and the issued certificate.
func writeClientKubeconfig(ca *pki.CA, kcDir, apiserverURL, clusterName, userName, commonName string, orgs []string) (string, *pki.Cert, error) {
	clientCert, err := ca.IssueClient(commonName, orgs)
	if err != nil {
		return "", nil, fmt.Errorf("bootstrap: issuing %s client cert: %w", userName, err)
	}

	path := filepath.Join(kcDir, userName+".kubeconfig")
	if err := pki.WriteKubeconfig(path, apiserverURL, ca, clientCert, clusterName, userName, clusterName); err != nil {
		return "", nil, fmt.Errorf("bootstrap: writing %s kubeconfig: %w", userName, err)
	}

	return path, clientCert, nil
}

func writePEM(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("bootstrap: writing %s: %w", path, err)
	}

	return nil
}
