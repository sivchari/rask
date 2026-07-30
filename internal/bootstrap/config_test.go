package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCNIConfig_ContainsPodCIDR(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := writeCNIConfig(dir); err != nil {
		t.Fatalf("writeCNIConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "10-rask.conflist"))
	if err != nil {
		t.Fatalf("reading conflist: %v", err)
	}

	if !strings.Contains(string(data), `"subnet": "10.244.0.0/16"`) {
		t.Errorf("conflist does not contain the expected pod CIDR subnet:\n%s", data)
	}

	if !strings.Contains(string(data), `"type": "bridge"`) || !strings.Contains(string(data), `"type": "portmap"`) {
		t.Errorf("conflist missing bridge or portmap plugin:\n%s", data)
	}
}

func TestWriteContainerdConfig_RendersRequiredFields(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	paths, err := writeContainerdConfig(dataDir, "/opt/rask/runc", "/opt/rask/cni-bin", "/opt/rask/cni-conf")
	if err != nil {
		t.Fatalf("writeContainerdConfig: %v", err)
	}

	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	content := string(data)
	for _, want := range []string{
		`BinaryName = "/opt/rask/runc"`,
		`bin_dir = "/opt/rask/cni-bin"`,
		`conf_dir = "/opt/rask/cni-conf"`,
		`sandbox = "registry.k8s.io/pause:3.10"`,
		"address = " + `"` + paths.socketPath + `"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("containerd config missing %q:\n%s", want, content)
		}
	}

	for _, dir := range []string{filepath.Dir(paths.configPath), paths.root, paths.state} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist", dir)
		}
	}
}

func TestWriteKubeletConfig_RendersRequiredFields(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	paths, err := writeKubeletConfig(dataDir, "/opt/rask/pki/ca.crt", "/opt/rask/containerd/containerd.sock")
	if err != nil {
		t.Fatalf("writeKubeletConfig: %v", err)
	}

	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	wantResolvConf := filepath.Join(dataDir, "kubelet", "resolv.conf")

	content := string(data)
	for _, want := range []string{
		`clientCAFile: "/opt/rask/pki/ca.crt"`,
		"containerRuntimeEndpoint: unix:///opt/rask/containerd/containerd.sock",
		`staticPodPath: ""`,
		"clusterDNS:\n  - 10.96.0.10",
		fmt.Sprintf("resolvConf: %q", wantResolvConf),
	} {
		if !strings.Contains(content, want) {
			t.Errorf("kubelet config missing %q:\n%s", want, content)
		}
	}
}

// TestWriteKubeletConfig_PodResolvConfIsNotTheHostsOwn is a regression test:
// resolvConf used to point straight at the host's own /etc/resolv.conf,
// which on colima/lima-style VMs has a nameserver equal to the node's own
// address — CoreDNS's forward-and-hairpin-through-the-CNI-bridge then
// triggers its own loop detection and CrashLoopBackOffs (found via a real
// E2E run). writeKubeletConfig must instead author its own resolv.conf
// pointing at public resolvers with no such self-reference.
func TestWriteKubeletConfig_PodResolvConfIsNotTheHostsOwn(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	paths, err := writeKubeletConfig(dataDir, "/opt/rask/pki/ca.crt", "/opt/rask/containerd/containerd.sock")
	if err != nil {
		t.Fatalf("writeKubeletConfig: %v", err)
	}

	configData, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	if strings.Contains(string(configData), "resolvConf: /etc/resolv.conf") {
		t.Fatal("kubelet config points resolvConf at the host's own /etc/resolv.conf")
	}

	resolvConfPath := filepath.Join(dataDir, "kubelet", "resolv.conf")

	resolvData, err := os.ReadFile(resolvConfPath)
	if err != nil {
		t.Fatalf("reading pod resolv.conf: %v", err)
	}

	if !strings.Contains(string(resolvData), "nameserver 8.8.8.8") {
		t.Errorf("pod resolv.conf = %q, want it to contain a public nameserver", resolvData)
	}
}

func TestWriteKubeProxyConfig_RendersRequiredFields(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	configPath, err := writeKubeProxyConfig(dataDir, "/opt/rask/kubeconfigs/kube-proxy.kubeconfig")
	if err != nil {
		t.Fatalf("writeKubeProxyConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	content := string(data)
	for _, want := range []string{
		`kubeconfig: "/opt/rask/kubeconfigs/kube-proxy.kubeconfig"`,
		"mode: iptables",
		`clusterCIDR: "10.244.0.0/16"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("kube-proxy config missing %q:\n%s", want, content)
		}
	}
}
