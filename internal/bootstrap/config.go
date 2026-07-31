package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
)

// cniConflistTemplate is the standard bridge/host-local/portmap CNI network
// containerd's CRI plugin expects. loopback is not referenced here:
// containerd invokes it internally for every pod sandbox's lo interface, so
// it only needs to be present in the CNI bin dir (see components.Paths.CNIBinDir).
const cniConflistTemplate = `{
  "cniVersion": "1.0.0",
  "name": "rask",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cni0",
      "isGateway": true,
      "ipMasq": true,
      "hairpinMode": true,
      "ipam": {
        "type": "host-local",
        "ranges": [[{"subnet": "%s"}]],
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    },
    {
      "type": "portmap",
      "capabilities": {"portMappings": true}
    }
  ]
}
`

// apiAudiences returns kube-apiserver's --api-audiences value: issuer (the
// cluster's own service-account issuer, always trusted) plus any
// caller-requested extra audiences, comma-joined. Extra audiences let a
// client request a projected ServiceAccount token for a custom audience
// (e.g. haro's TokenReview-based phone-home uses "haro") that the API
// server will still accept at authentication time.
func apiAudiences(issuer string, extra []string) string {
	return strings.Join(append([]string{issuer}, extra...), ",")
}

// buildAPIServerArgs appends every caller-supplied "key=value" entry in
// extraArgs (StartOptions.ExtraAPIServerArgs / "rask create --apiserver-arg",
// kubeadm's extraArgs shape) to base, rask's own required kube-apiserver
// flags, and returns the combined argument list.
//
// A key that names one of the flags already in base is rejected with a
// clear error instead of silently letting a later occurrence win. That is
// the opposite of kubeadm's own extraArgs (which happily lets a caller
// override anything kubeadm generates): every flag in base is load-bearing
// for rask's own boot DAG in a way that depends on its *exact* resolved
// value — the readiness probe URL is built from the same secure-port
// constant passed via --secure-port, TLS trust throughout Boot assumes
// --tls-cert-file/--client-ca-file are what was actually passed, and
// --anonymous-auth=false/--authorization-mode=Node,RBAC are security
// invariants, not defaults meant to be tweakable. Silently losing one of
// them to a later "--flag=value wins" apiserver flag-parsing rule would
// either break boot in a confusing way or silently regress a security
// posture; kubeadm has no equivalent downstream code depending on its own
// generated flags' exact values, so it can afford to allow it.
func buildAPIServerArgs(base, extraArgs []string) ([]string, error) {
	managed := make(map[string]bool, len(base))
	for _, a := range base {
		managed[apiServerArgKey(a)] = true
	}

	args := append([]string{}, base...)

	for _, e := range extraArgs {
		key, value, ok := strings.Cut(e, "=")
		if !ok {
			return nil, fmt.Errorf("bootstrap: invalid --apiserver-arg %q: must be key=value", e)
		}

		key = strings.TrimPrefix(key, "--")
		if managed[key] {
			return nil, fmt.Errorf("bootstrap: --apiserver-arg %q: %q is a rask-managed flag and cannot be overridden (use its dedicated rask flag instead, e.g. --api-audience for api-audiences)", e, key)
		}

		args = append(args, "--"+key+"="+value)
	}

	return args, nil
}

// apiServerArgKey extracts the flag name (without a leading "--" or
// trailing "=value") from a rendered "--key=value" (or "--key") apiserver
// argument.
func apiServerArgKey(arg string) string {
	key := strings.TrimPrefix(arg, "--")
	if idx := strings.IndexByte(key, '='); idx >= 0 {
		key = key[:idx]
	}

	return key
}

// writeCNIConfig writes a bridge+host-local+portmap conflist to confDir.
func writeCNIConfig(confDir string) error {
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: creating CNI conf dir: %w", err)
	}

	content := fmt.Sprintf(cniConflistTemplate, cluster.PodCIDR)
	path := filepath.Join(confDir, "10-rask.conflist")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("bootstrap: writing CNI conflist: %w", err)
	}

	return nil
}

// containerdConfigTemplate targets containerd 2.x's split CRI plugin schema
// (io.containerd.cri.v1.images / io.containerd.cri.v1.runtime, not the
// single io.containerd.grpc.v1.cri block from containerd 1.x). Only fields
// that must diverge from containerd's built-in defaults are set.
const containerdConfigTemplate = `version = 4
root = %q
state = %q
imports = []

[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'overlayfs'

  [plugins.'io.containerd.cri.v1.images'.pinned_images]
    sandbox = %q

[plugins.'io.containerd.cri.v1.runtime']
  [plugins.'io.containerd.cri.v1.runtime'.containerd]
    default_runtime_name = 'runc'

    [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
      runtime_type = 'io.containerd.runc.v2'

      [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]
        BinaryName = %q
        SystemdCgroup = false

  [plugins.'io.containerd.cri.v1.runtime'.cni]
    bin_dir = %q
    conf_dir = %q
    max_conf_num = 1

[plugins.'io.containerd.server.v1.grpc']
  address = %q
  uid = 0
  gid = 0
`

// containerdPaths are the filesystem locations writeContainerdConfig
// resolves and creates under dataDir.
type containerdPaths struct {
	configPath string
	socketPath string
	root       string
	state      string
}

// writeContainerdConfig renders config.toml for a standalone containerd
// instance scoped entirely to dataDir, so it never touches any
// containerd/docker state already running on the host.
func writeContainerdConfig(dataDir, runcPath, cniBinDir, cniConfDir string) (*containerdPaths, error) {
	base := filepath.Join(dataDir, "containerd")
	paths := &containerdPaths{
		configPath: filepath.Join(base, "config.toml"),
		socketPath: filepath.Join(base, "containerd.sock"),
		root:       filepath.Join(base, "root"),
		state:      filepath.Join(base, "state"),
	}

	for _, dir := range []string{base, paths.root, paths.state} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("bootstrap: creating containerd dir %s: %w", dir, err)
		}
	}

	content := fmt.Sprintf(containerdConfigTemplate,
		paths.root, paths.state, components.PauseImage, runcPath, cniBinDir, cniConfDir, paths.socketPath,
	)

	if err := os.WriteFile(paths.configPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("bootstrap: writing containerd config: %w", err)
	}

	return paths, nil
}

// kubeletConfigTemplate is a KubeletConfiguration. staticPodPath is
// intentionally omitted: rask's boot DAG launches every component
// directly, so kubelet never needs to reconcile static pod manifests.
// serverTLSBootstrap is off so kubelet self-signs its serving cert instead
// of round-tripping a CSR through the API server. resolvConf points at a
// rask-authored file (see podResolvConf), not the host's own
// /etc/resolv.conf — see that constant's doc comment for why.
const kubeletConfigTemplate = `apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
staticPodPath: ""
authentication:
  anonymous:
    enabled: false
  webhook:
    enabled: false
  x509:
    clientCAFile: %q
authorization:
  mode: AlwaysAllow
serverTLSBootstrap: false
failSwapOn: false
cgroupDriver: cgroupfs
nodeStatusUpdateFrequency: 2s
resolvConf: %q
containerRuntimeEndpoint: unix://%s
clusterDomain: %q
clusterDNS:
  - %s
`

// podResolvConfTemplate is written to dataDir/kubelet/resolv.conf and used
// as kubelet's --resolv-conf, instead of the host's own /etc/resolv.conf.
//
// On at least colima/lima-style VMs, the host's resolv.conf nameserver is
// the VM's own primary address (the same address kubelet advertises as the
// node's InternalIP). CoreDNS's `forward . /etc/resolv.conf` then sends
// upstream queries to that address; with the bridge CNI plugin's
// hairpinMode+ipMasq, that traffic hairpins back through the node's own
// CNI bridge instead of actually leaving, and CoreDNS's own loop-detection
// (a periodic HINFO self-probe) correctly kills the pod as a real DNS
// loop — found via a real E2E run (CoreDNS CrashLoopBackOff,
// "[FATAL] plugin/loop: Loop ... detected"). Public resolvers have no such
// self-reference, so they can't hairpin-loop this way; hardcoding them
// here is a deliberate, environment-specific workaround for a local dev
// cluster (documented, not a silent hack), not a general recommendation.
const podResolvConfTemplate = "nameserver 8.8.8.8\nnameserver 1.1.1.1\n"

// kubeletPaths are the filesystem locations writeKubeletConfig resolves and
// creates under dataDir.
type kubeletPaths struct {
	configPath string
	rootDir    string
	certDir    string
}

// writeKubeletConfig renders the kubelet KubeletConfiguration file under
// dataDir.
func writeKubeletConfig(dataDir, caCertPath, containerdSocket string) (*kubeletPaths, error) {
	base := filepath.Join(dataDir, "kubelet")
	paths := &kubeletPaths{
		configPath: filepath.Join(base, "config.yaml"),
		rootDir:    filepath.Join(base, "root"),
		certDir:    filepath.Join(base, "pki"),
	}

	for _, dir := range []string{base, paths.rootDir, paths.certDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("bootstrap: creating kubelet dir %s: %w", dir, err)
		}
	}

	resolvConfPath := filepath.Join(base, "resolv.conf")
	if err := os.WriteFile(resolvConfPath, []byte(podResolvConfTemplate), 0o644); err != nil {
		return nil, fmt.Errorf("bootstrap: writing pod resolv.conf: %w", err)
	}

	content := fmt.Sprintf(kubeletConfigTemplate, caCertPath, resolvConfPath, containerdSocket, cluster.Domain, cluster.DNSServiceIP)

	if err := os.WriteFile(paths.configPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("bootstrap: writing kubelet config: %w", err)
	}

	return paths, nil
}

// kubeProxyConfigTemplate is a standard KubeProxyConfiguration in iptables
// mode. rask runs kube-proxy as a supervised host process rather than a
// DaemonSet pod (see boot.go's kube-proxy phase comment for why), but the
// configuration shape itself is the same one a DaemonSet would mount from a
// ConfigMap, so it stays portable if rask grows a DaemonSet form later.
const kubeProxyConfigTemplate = `apiVersion: kubeproxy.config.k8s.io/v1alpha1
kind: KubeProxyConfiguration
clientConnection:
  kubeconfig: %q
mode: iptables
clusterCIDR: %q
`

// writeKubeProxyConfig renders KubeProxyConfiguration under dataDir.
func writeKubeProxyConfig(dataDir, kubeconfigPath string) (string, error) {
	base := filepath.Join(dataDir, "kube-proxy")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("bootstrap: creating kube-proxy dir %s: %w", base, err)
	}

	configPath := filepath.Join(base, "config.yaml")
	content := fmt.Sprintf(kubeProxyConfigTemplate, kubeconfigPath, cluster.PodCIDR)

	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("bootstrap: writing kube-proxy config: %w", err)
	}

	return configPath, nil
}
