package guestlayout

import (
	"strings"
	"testing"
)

func TestPaths_EveryFieldUnderBinDirOrCNIBinDir(t *testing.T) {
	t.Parallel()

	p := Paths()

	fields := map[string]string{
		"KubeAPIServer":         p.KubeAPIServer,
		"KubeControllerManager": p.KubeControllerManager,
		"KubeScheduler":         p.KubeScheduler,
		"Kubelet":               p.Kubelet,
		"KubeProxy":             p.KubeProxy,
		"Kubectl":               p.Kubectl,
		"Kine":                  p.Kine,
		"Containerd":            p.Containerd,
		"Runc":                  p.Runc,
	}

	for name, path := range fields {
		if !strings.HasPrefix(path, BinDir+"/") {
			t.Errorf("%s = %q, want a path under %s", name, path, BinDir)
		}
	}

	if p.ContainerdBinDir != BinDir {
		t.Errorf("ContainerdBinDir = %q, want %q (containerd's shim discovery requires this)", p.ContainerdBinDir, BinDir)
	}

	if p.CNIBinDir != CNIBinDir {
		t.Errorf("CNIBinDir = %q, want %q", p.CNIBinDir, CNIBinDir)
	}
}
