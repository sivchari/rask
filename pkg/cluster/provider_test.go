package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/prebake"
	"github.com/sivchari/rask/internal/substrate"
)

func TestProvider_Create_AlreadyExistsRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding existing cluster dir: %v", err)
	}

	rt := &fakeRuntime{}
	p := NewProviderWithRuntime(rt, homeDir)

	if _, err := p.Create(context.Background(), "dev", Options{}); err == nil {
		t.Fatal("Create() on an existing cluster = nil error, want error")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want none", rt.createCalls)
	}
}

func TestProvider_Create_InvalidWaitRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	rt := &fakeRuntime{}
	p := NewProviderWithRuntime(rt, t.TempDir())

	if _, err := p.Create(context.Background(), "dev", Options{Wait: "bogus"}); err == nil {
		t.Fatal("Create() with an invalid Wait = nil error, want error")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want none", rt.createCalls)
	}
}

func TestProvider_Create_PassesSeamsThroughToStartOptions(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	rt := &fakeRuntime{onStart: func(name string) error { return writeFakeKubeconfig(homeDir, name) }}
	p := NewProviderWithRuntime(rt, homeDir)

	opts := Options{
		ExtraAPIAudiences:  []string{"haro"},
		ExtraAPIServerArgs: []string{"authentication-token-webhook-config-file=/tmp/webhook.yaml"},
		PrebootFiles:       []PrebootFile{{Src: "/tmp/webhook-kubeconfig.yaml", Dest: "auth/webhook.yaml"}},
		ComponentDir:       "/opt/eksd/bin",
		CoreDNSImage:       "example.com/coredns:v1",
	}

	result, err := p.Create(context.Background(), "dev", opts)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(rt.createOptsCalls) != 1 || len(rt.startOptsCalls) != 1 {
		t.Fatalf("createOptsCalls = %v, startOptsCalls = %v, want exactly 1 call each", rt.createOptsCalls, rt.startOptsCalls)
	}

	for _, got := range []substrate.StartOptions{rt.createOptsCalls[0], rt.startOptsCalls[0]} {
		if got.ComponentDir != opts.ComponentDir {
			t.Errorf("ComponentDir = %q, want %q", got.ComponentDir, opts.ComponentDir)
		}

		if got.CoreDNSImage != opts.CoreDNSImage {
			t.Errorf("CoreDNSImage = %q, want %q", got.CoreDNSImage, opts.CoreDNSImage)
		}

		if len(got.ExtraAPIServerArgs) != 1 || got.ExtraAPIServerArgs[0] != opts.ExtraAPIServerArgs[0] {
			t.Errorf("ExtraAPIServerArgs = %v, want %v", got.ExtraAPIServerArgs, opts.ExtraAPIServerArgs)
		}

		if len(got.PrebootFiles) != 1 || got.PrebootFiles[0].Src != "/tmp/webhook-kubeconfig.yaml" || got.PrebootFiles[0].Dest != "auth/webhook.yaml" {
			t.Errorf("PrebootFiles = %v, want a single matching entry", got.PrebootFiles)
		}

		if len(got.ExtraAPIAudiences) != 1 || got.ExtraAPIAudiences[0] != "haro" {
			t.Errorf("ExtraAPIAudiences = %v, want [haro]", got.ExtraAPIAudiences)
		}
	}

	wantKubeconfig := filepath.Join(homeDir, "clusters", "dev", "kubeconfig")
	if result.KubeconfigPath != wantKubeconfig {
		t.Errorf("Result.KubeconfigPath = %q, want %q", result.KubeconfigPath, wantKubeconfig)
	}
}

func TestProvider_Create_UsesMatchingSeedForRequestedCoreDNSImage(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	const overrideImage = "example.com/coredns:v1"

	seedPath := prebake.Path(homeDir, components.DefaultK8sVersion, overrideImage)
	if err := os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
		t.Fatalf("seeding seed dir: %v", err)
	}

	if err := os.WriteFile(seedPath, []byte("fake seed"), 0o644); err != nil {
		t.Fatalf("writing fake seed: %v", err)
	}

	rt := &fakeRuntime{onStart: func(name string) error { return writeFakeKubeconfig(homeDir, name) }}
	p := NewProviderWithRuntime(rt, homeDir)

	if _, err := p.Create(context.Background(), "dev", Options{CoreDNSImage: overrideImage}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	if got := rt.startOptsCalls[0].SeedPath; got != seedPath {
		t.Errorf("StartOptions.SeedPath = %q, want %q", got, seedPath)
	}
}

func TestProvider_Create_DefaultCoreDNSImageDoesNotMatchASeedBuiltForAnOverride(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// A seed built for a non-default image must not be picked up for a
	// plain (default-image) create request.
	seedPath := prebake.Path(homeDir, components.DefaultK8sVersion, "example.com/coredns:v1")
	if err := os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
		t.Fatalf("seeding seed dir: %v", err)
	}

	if err := os.WriteFile(seedPath, []byte("fake seed"), 0o644); err != nil {
		t.Fatalf("writing fake seed: %v", err)
	}

	rt := &fakeRuntime{onStart: func(name string) error { return writeFakeKubeconfig(homeDir, name) }}
	p := NewProviderWithRuntime(rt, homeDir)

	if _, err := p.Create(context.Background(), "dev", Options{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := rt.startOptsCalls[0].SeedPath; got != "" {
		t.Errorf("StartOptions.SeedPath = %q, want empty (only a differently-imaged seed exists)", got)
	}
}

func TestProvider_Create_StartFailureLeavesNoStateDirectory(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	wantErr := errors.New("start boom")

	rt := &fakeRuntime{startErr: wantErr}
	p := NewProviderWithRuntime(rt, homeDir)

	_, err := p.Create(context.Background(), "dev", Options{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create() error = %v, want to wrap %v", err, wantErr)
	}

	if _, statErr := os.Stat(filepath.Join(homeDir, "clusters", "dev")); !os.IsNotExist(statErr) {
		t.Error("cluster state directory exists despite Start failing")
	}
}

func TestProvider_Delete_NotExistsRejects(t *testing.T) {
	t.Parallel()

	rt := &fakeRuntime{}
	p := NewProviderWithRuntime(rt, t.TempDir())

	if err := p.Delete(context.Background(), "dev"); err == nil {
		t.Fatal("Delete() on a non-existent cluster = nil error, want error")
	}
}

func TestProvider_Delete_StopsThenDeletesThenRemovesStateDir(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	rt := &fakeRuntime{}
	p := NewProviderWithRuntime(rt, homeDir)

	if err := p.Delete(context.Background(), "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(rt.stopCalls) != 1 || len(rt.deleteCalls) != 1 {
		t.Errorf("stopCalls = %v, deleteCalls = %v, want exactly 1 each", rt.stopCalls, rt.deleteCalls)
	}

	if _, err := os.Stat(filepath.Join(homeDir, "clusters", "dev")); !os.IsNotExist(err) {
		t.Error("cluster state directory still exists after Delete")
	}
}

func TestProvider_List_ReturnsClusterNames(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	for _, name := range []string{"b-cluster", "a-cluster"} {
		if err := os.MkdirAll(filepath.Join(homeDir, "clusters", name), 0o755); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}

	p := NewProviderWithRuntime(&fakeRuntime{}, homeDir)

	got, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"a-cluster", "b-cluster"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

func TestProvider_KubeConfigPath_MatchesConvention(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	p := NewProviderWithRuntime(&fakeRuntime{}, homeDir)

	got := p.KubeConfigPath("dev")
	want := filepath.Join(homeDir, "clusters", "dev", "kubeconfig")

	if got != want {
		t.Errorf("KubeConfigPath() = %q, want %q", got, want)
	}
}

func TestProvider_KubeConfig_RenamesContextPerFormat(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	if err := writeFakeKubeconfig(homeDir, "dev"); err != nil {
		t.Fatalf("writeFakeKubeconfig: %v", err)
	}

	p := NewProviderWithRuntime(&fakeRuntime{}, homeDir)

	data, err := p.KubeConfig("dev", ExportOptions{ContextFormat: "kind-{name}"})
	if err != nil {
		t.Fatalf("KubeConfig: %v", err)
	}

	if !strings.Contains(string(data), "kind-dev") {
		t.Errorf("KubeConfig() output does not contain the renamed context %q:\n%s", "kind-dev", data)
	}
}

func TestProvider_KubeConfig_EmptyFormatLeavesContextUnchanged(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	if err := writeFakeKubeconfig(homeDir, "dev"); err != nil {
		t.Fatalf("writeFakeKubeconfig: %v", err)
	}

	p := NewProviderWithRuntime(&fakeRuntime{}, homeDir)

	data, err := p.KubeConfig("dev", ExportOptions{})
	if err != nil {
		t.Fatalf("KubeConfig: %v", err)
	}

	if !strings.Contains(string(data), "current-context: dev") {
		t.Errorf("KubeConfig() output changed the context name despite an empty ContextFormat:\n%s", data)
	}
}

func TestProvider_KubeConfig_NotExistsRejects(t *testing.T) {
	t.Parallel()

	p := NewProviderWithRuntime(&fakeRuntime{}, t.TempDir())

	if _, err := p.KubeConfig("dev", ExportOptions{}); err == nil {
		t.Fatal("KubeConfig() for a non-existent cluster = nil error, want error")
	}
}

func TestProvider_LoadImages_PassesReferencesThroughAndRequiresExistingCluster(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	rt := &fakeRuntime{}
	p := NewProviderWithRuntime(rt, homeDir)

	if err := p.LoadImages(context.Background(), "dev", []ImageSource{{Reference: "nginx:latest", Stream: strings.NewReader("fake archive")}}); err == nil {
		t.Fatal("LoadImages() on a non-existent cluster = nil error, want error")
	}

	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	if err := p.LoadImages(context.Background(), "dev", []ImageSource{{Reference: "nginx:latest", Stream: strings.NewReader("fake archive")}}); err != nil {
		t.Fatalf("LoadImages: %v", err)
	}

	if len(rt.loadImagesCalls) != 1 || len(rt.loadImagesCalls[0].references) != 1 || rt.loadImagesCalls[0].references[0] != "nginx:latest" {
		t.Errorf("loadImagesCalls = %+v, want a single call referencing nginx:latest", rt.loadImagesCalls)
	}
}

func TestProvider_PortForward_PassesArgumentsThroughAndRequiresExistingCluster(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	rt := &fakeRuntime{portForwardErrCh: make(chan error)}
	p := NewProviderWithRuntime(rt, homeDir)

	if _, _, err := p.PortForward(context.Background(), "dev", "127.0.0.1:0", "10.0.0.5:80"); err == nil {
		t.Fatal("PortForward() on a non-existent cluster = nil error, want error")
	}

	if len(rt.portForwardCalls) != 0 {
		t.Errorf("portForwardCalls = %+v, want no calls before the cluster exists", rt.portForwardCalls)
	}

	if err := os.MkdirAll(filepath.Join(homeDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	boundAddr, errs, err := p.PortForward(context.Background(), "dev", "127.0.0.1:0", "10.0.0.5:80")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}

	if boundAddr != "127.0.0.1:41000" {
		t.Errorf("boundAddr = %q, want rt's return value forwarded verbatim", boundAddr)
	}

	if errs != (<-chan error)(rt.portForwardErrCh) {
		t.Error("errs channel is not the same channel rt.PortForward returned")
	}

	want := []portForwardCall{{name: "dev", localAddr: "127.0.0.1:0", remoteAddr: "10.0.0.5:80"}}
	if len(rt.portForwardCalls) != 1 || rt.portForwardCalls[0] != want[0] {
		t.Errorf("portForwardCalls = %+v, want %+v", rt.portForwardCalls, want)
	}
}

// TestProvider_DefaultCoreDNSImageMatchesManifestsPackage guards against
// this package's default-image fallback in Create silently drifting from
// internal/manifests.CoreDNSImage.
func TestProvider_DefaultCoreDNSImageMatchesManifestsPackage(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	seedPath := prebake.Path(homeDir, components.DefaultK8sVersion, manifests.CoreDNSImage)
	if err := os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
		t.Fatalf("seeding seed dir: %v", err)
	}

	if err := os.WriteFile(seedPath, []byte("fake seed"), 0o644); err != nil {
		t.Fatalf("writing fake seed: %v", err)
	}

	rt := &fakeRuntime{onStart: func(name string) error { return writeFakeKubeconfig(homeDir, name) }}
	p := NewProviderWithRuntime(rt, homeDir)

	if _, err := p.Create(context.Background(), "dev", Options{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := rt.startOptsCalls[0].SeedPath; got != seedPath {
		t.Errorf("StartOptions.SeedPath = %q, want %q (Create's default fallback must match manifests.CoreDNSImage)", got, seedPath)
	}
}
