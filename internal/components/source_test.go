package components

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedNonK8sCache pre-populates cacheDir with a cache hit for every
// non-Kubernetes component (kine, runc, containerd, cni-plugins) plus
// kube-proxy, for arch — matching the layout ensureFile/ensureArchive
// check for "already cached" (see ensure.go), so Ensure/ensureNonOverridden
// take the cache-hit path instead of attempting a real network call. Mirrors
// the established pattern in kernel_test.go/iptables_test.go's
// "CacheHitSkipsDownload" tests.
func seedNonK8sCache(t *testing.T, cacheDir string, k8sVersion string, arch Arch) {
	t.Helper()

	writeFile(t, filepath.Join(cacheDir, "k8s-"+k8sVersion+"-"+string(arch), "kube-proxy"), "cached kube-proxy")
	writeFile(t, filepath.Join(cacheDir, "kine-"+KineVersion+"-"+string(arch), "kine"), "cached kine")
	writeFile(t, filepath.Join(cacheDir, "runc-"+RuncVersion+"-"+string(arch), "runc"), "cached runc")

	containerdDir := filepath.Join(cacheDir, "containerd-"+ContainerdVersion+"-"+string(arch))
	writeFile(t, filepath.Join(containerdDir, "bin", "containerd"), "cached containerd")

	cniDir := filepath.Join(cacheDir, "cni-plugins-"+CNIPluginsVersion+"-"+string(arch))
	writeFile(t, filepath.Join(cniDir, "bridge"), "cached bridge")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// writeLocalDirBinaries creates every LocalDirSource-expected binary under
// dir, executable.
func writeLocalDirBinaries(t *testing.T, dir string) {
	t.Helper()

	for _, bin := range localDirK8sBinaries {
		writeFile(t, filepath.Join(dir, bin), "local:"+bin)
	}
}

func TestDownloadCacheSource_Resolve_CacheHitSkipsDownload(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	seedNonK8sCache(t, cacheDir, "v1.33.13", ARM64)

	for _, bin := range k8sBinaries {
		writeFile(t, filepath.Join(cacheDir, "k8s-v1.33.13-arm64", bin), "cached:"+bin)
	}

	src := NewDownloadCacheSource(NewCache(cacheDir))

	// c.client is http.DefaultClient with nothing reachable behind it in
	// this test; a cache miss would hang/fail on a real network call, so
	// success here proves the cache-hit path took priority (same reasoning
	// as TestEnsureGuestKernel_CacheHitSkipsDownload).
	paths, err := src.Resolve(context.Background(), "v1.33.13", ARM64)
	if err != nil {
		t.Fatalf("Resolve (cache hit): %v", err)
	}

	want := filepath.Join(cacheDir, "k8s-v1.33.13-arm64", "kube-apiserver")
	if paths.KubeAPIServer != want {
		t.Errorf("KubeAPIServer = %q, want %q", paths.KubeAPIServer, want)
	}
}

func TestLocalDirSource_Resolve_OverlaysLocalBinariesOntoCacheDefaults(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	seedNonK8sCache(t, cacheDir, "v1.33.13", ARM64)

	dir := t.TempDir()
	writeLocalDirBinaries(t, dir)

	src := NewLocalDirSource(dir, NewCache(cacheDir))

	paths, err := src.Resolve(context.Background(), "v1.33.13", ARM64)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for _, tt := range []struct {
		name string
		got  string
	}{
		{"KubeAPIServer", paths.KubeAPIServer},
		{"KubeControllerManager", paths.KubeControllerManager},
		{"KubeScheduler", paths.KubeScheduler},
		{"Kubelet", paths.Kubelet},
		{"Kubectl", paths.Kubectl},
	} {
		if filepath.Dir(tt.got) != dir {
			t.Errorf("%s = %q, want it resolved from local dir %q", tt.name, tt.got, dir)
		}
	}

	// kube-proxy is never overridden by LocalDirSource (see its doc
	// comment): it must still come from the wrapped Cache's download
	// directory, not dir.
	if filepath.Dir(paths.KubeProxy) == dir {
		t.Errorf("KubeProxy = %q, want it resolved from the default cache, not the local dir", paths.KubeProxy)
	}

	if paths.Kine == "" || paths.Runc == "" || paths.Containerd == "" || paths.CNIBinDir == "" {
		t.Errorf("Resolve() left a non-k8s component path empty: %+v", paths)
	}
}

func TestLocalDirSource_Resolve_MissingBinaryFailsFastWithClearError(t *testing.T) {
	t.Parallel()

	cache := NewCache(t.TempDir())

	dir := t.TempDir()
	writeLocalDirBinaries(t, dir)

	if err := os.Remove(filepath.Join(dir, "kubelet")); err != nil {
		t.Fatalf("removing kubelet: %v", err)
	}

	src := NewLocalDirSource(dir, cache)

	_, err := src.Resolve(context.Background(), "v1.33.13", ARM64)
	if err == nil {
		t.Fatal("Resolve() with a missing binary = nil error, want error")
	}

	if !strings.Contains(err.Error(), "kubelet") {
		t.Errorf("Resolve() error = %v, want it to name the missing binary %q", err, "kubelet")
	}
}

func TestLocalDirSource_Resolve_NonExecutableBinaryFailsFastWithClearError(t *testing.T) {
	t.Parallel()

	cache := NewCache(t.TempDir())

	dir := t.TempDir()
	writeLocalDirBinaries(t, dir)

	if err := os.Chmod(filepath.Join(dir, "kube-apiserver"), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	src := NewLocalDirSource(dir, cache)

	_, err := src.Resolve(context.Background(), "v1.33.13", ARM64)
	if err == nil {
		t.Fatal("Resolve() with a non-executable binary = nil error, want error")
	}

	if !strings.Contains(err.Error(), "kube-apiserver") || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("Resolve() error = %v, want it to name kube-apiserver as not executable", err)
	}
}

func TestLocalDirSource_Resolve_ReportsEveryMissingBinaryAtOnce(t *testing.T) {
	t.Parallel()

	cache := NewCache(t.TempDir())
	dir := t.TempDir() // empty: every expected binary is missing

	src := NewLocalDirSource(dir, cache)

	_, err := src.Resolve(context.Background(), "v1.33.13", ARM64)
	if err == nil {
		t.Fatal("Resolve() with an empty dir = nil error, want error")
	}

	for _, bin := range localDirK8sBinaries {
		if !strings.Contains(err.Error(), bin) {
			t.Errorf("Resolve() error does not mention missing binary %q:\n%v", bin, err)
		}
	}
}
