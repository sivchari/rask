package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// kubeletConfigTemplate is a KubeletConfiguration. staticPodPath is
// intentionally omitted (empty) since this spike runs no static pods.
// serverTLSBootstrap is off so kubelet self-signs its serving cert instead
// of round-tripping a CSR through the apiserver. nodeStatusUpdateFrequency
// is tuned down from the 10s default: kubelet's fastStatusUpdateOnce path
// already accelerates the very first Ready transition, but a low steady
// cadence removes any doubt it's on the critical path.
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
resolvConf: /etc/resolv.conf
containerRuntimeEndpoint: unix://%s
`

type kubeletPaths struct {
	configPath string
	rootDir    string
	certDir    string
}

// writeKubeletConfig renders the kubelet KubeletConfiguration file under
// datadir.
func writeKubeletConfig(datadir, caCertPath, containerdSocket string) (*kubeletPaths, error) {
	base := filepath.Join(datadir, "kubelet")
	paths := &kubeletPaths{
		configPath: filepath.Join(base, "config.yaml"),
		rootDir:    filepath.Join(base, "root"),
		certDir:    filepath.Join(base, "pki"),
	}

	for _, dir := range []string{base, paths.rootDir, paths.certDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating kubelet dir %s: %w", dir, err)
		}
	}

	content := fmt.Sprintf(kubeletConfigTemplate, caCertPath, containerdSocket)
	if err := os.WriteFile(paths.configPath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("writing kubelet config: %w", err)
	}

	return paths, nil
}
