//go:build linux

package hostproc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// bridgeNFCallIptables is the sysctl that makes traffic crossing a Linux
// bridge traverse iptables. It only exists once the br_netfilter kernel
// module is loaded.
const bridgeNFCallIptables = "/proc/sys/net/bridge/bridge-nf-call-iptables"

// ensureBridgeNetfilter guarantees bridge-nf-call-iptables=1 before any
// pod traffic can flow, loading br_netfilter if needed. Without it,
// pod-to-pod traffic on the same cni0 bridge is plain L2 forwarding that
// never enters netfilter, so kube-proxy and any NetworkPolicy enforcer are
// silently bypassed — found live: a default-deny NetworkPolicy let
// cross-namespace pod-IP connections straight through until the module was
// loaded. kind guarantees the same thing in its node entrypoint; the vz
// substrate ships br_netfilter in its initramfs module list
// (internal/guestinit.WantedModules), which defaults the sysctl to 1 on
// load, so hostproc is the only substrate that has to enforce it itself.
func ensureBridgeNetfilter() error {
	return ensureBridgeNetfilterFile(bridgeNFCallIptables, func() {
		// Best-effort: on a real host this loads the module; inside a
		// container there is usually no module tree to load from, and the
		// re-check below produces better guidance than modprobe's error.
		_ = exec.Command("modprobe", "br_netfilter").Run()
	})
}

// ensureBridgeNetfilterFile is ensureBridgeNetfilter with the sysctl path
// and module loader injected so the logic is unit-testable.
func ensureBridgeNetfilterFile(path string, loadModule func()) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		loadModule()
	}

	value, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hostproc: %s does not exist: the br_netfilter kernel module is not loaded and could not be loaded from here; load it where the kernel lives (modprobe br_netfilter — from inside a container that means on the container host), or bridged pod-to-pod traffic bypasses iptables and NetworkPolicy is silently unenforced", path)
	}

	if err != nil {
		return fmt.Errorf("hostproc: reading %s: %w", path, err)
	}

	if strings.TrimSpace(string(value)) == "1" {
		return nil
	}

	if err := os.WriteFile(filepath.Clean(path), []byte("1\n"), 0o600); err != nil {
		return fmt.Errorf("hostproc: enabling %s: %w", path, err)
	}

	return nil
}
