package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// cniConflist is the standard containerd "getting started" bridge network,
// using the bridge/host-local/portmap CNI plugins. loopback is not
// referenced here: containerd's CRI plugin invokes it internally for every
// pod sandbox's lo interface, so it only needs to be present in bin_dir.
const cniConflist = `{
  "cniVersion": "1.0.0",
  "name": "rask-spike-s1",
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

// writeCNIConfig writes a bridge+host-local+portmap conflist to confDir.
// binDir must already contain the bridge/host-local/loopback/portmap
// plugin binaries (see fetch.sh).
func writeCNIConfig(confDir string) error {
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return fmt.Errorf("creating CNI conf dir: %w", err)
	}
	content := fmt.Sprintf(cniConflist, podCIDR)
	path := filepath.Join(confDir, "10-rask-spike-s1.conflist")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing CNI conflist: %w", err)
	}
	return nil
}
