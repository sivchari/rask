package components

import "testing"

// TestComponentURLs pins the URL patterns proven reachable (200/302, real
// checksum content fetched via curl) against dl.k8s.io / GitHub Releases
// during implementation, so a typo doesn't silently break "rask pull".
func TestComponentURLs(t *testing.T) {
	t.Parallel()

	if got, want := k8sBinaryURL("v1.33.13", ARM64, "kubelet"), "https://dl.k8s.io/v1.33.13/bin/linux/arm64/kubelet"; got != want {
		t.Errorf("k8sBinaryURL = %q, want %q", got, want)
	}

	if got, want := k8sChecksumURL("v1.33.13", AMD64, "kubelet"), "https://dl.k8s.io/v1.33.13/bin/linux/amd64/kubelet.sha256"; got != want {
		t.Errorf("k8sChecksumURL = %q, want %q", got, want)
	}

	if got, want := kineURL(ARM64), "https://github.com/k3s-io/kine/releases/download/v0.16.3/kine-arm64"; got != want {
		t.Errorf("kineURL = %q, want %q", got, want)
	}

	if got, want := kineChecksumURL(AMD64), "https://github.com/k3s-io/kine/releases/download/v0.16.3/sha256sum-amd64.txt"; got != want {
		t.Errorf("kineChecksumURL = %q, want %q", got, want)
	}

	if got, want := runcURL(ARM64), "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc.arm64"; got != want {
		t.Errorf("runcURL = %q, want %q", got, want)
	}

	if got, want := runcChecksumURL(), "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc.sha256sum"; got != want {
		t.Errorf("runcChecksumURL = %q, want %q", got, want)
	}

	if got, want := containerdURL(ARM64), "https://github.com/containerd/containerd/releases/download/v2.3.3/containerd-2.3.3-linux-arm64.tar.gz"; got != want {
		t.Errorf("containerdURL = %q, want %q", got, want)
	}

	if got, want := containerdChecksumURL(AMD64), "https://github.com/containerd/containerd/releases/download/v2.3.3/containerd-2.3.3-linux-amd64.tar.gz.sha256sum"; got != want {
		t.Errorf("containerdChecksumURL = %q, want %q", got, want)
	}

	if got, want := cniURL(ARM64), "https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-arm64-v1.9.1.tgz"; got != want {
		t.Errorf("cniURL = %q, want %q", got, want)
	}

	if got, want := cniChecksumURL(AMD64), "https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-amd64-v1.9.1.tgz.sha256"; got != want {
		t.Errorf("cniChecksumURL = %q, want %q", got, want)
	}
}

func TestHostArch(t *testing.T) {
	t.Parallel()

	arch, err := HostArch()
	if err != nil {
		t.Fatalf("HostArch: %v", err)
	}

	if arch != ARM64 && arch != AMD64 {
		t.Errorf("HostArch() = %q, want arm64 or amd64", arch)
	}
}
