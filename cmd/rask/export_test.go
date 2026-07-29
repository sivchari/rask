package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/pki"
)

// seedKubeconfig writes a kubeconfig for name into its state directory
// under homeDir, mimicking what a real "create cluster" will eventually
// produce: cluster/user/context entries all keyed by the cluster name.
func seedKubeconfig(t *testing.T, homeDir, name string) {
	t.Helper()

	ca, err := pki.NewCA("rask-ca")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	clientCert, err := ca.IssueClient(name, []string{"system:masters"})
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}

	mustMkdirAll(t, cluster.Dir(homeDir, name))

	path := filepath.Join(cluster.Dir(homeDir, name), "kubeconfig")
	if err := pki.WriteKubeconfig(path, "https://127.0.0.1:6443", ca, clientCert, name, name, name); err != nil {
		t.Fatalf("WriteKubeconfig: %v", err)
	}
}

func TestExportKubeconfig_MissingClusterErrors(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"export", "kubeconfig", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want does-not-exist error")
	}
}

func TestExportKubeconfig_DefaultContextFormatToStdout(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	seedKubeconfig(t, homeDir, "dev")

	out := &bytes.Buffer{}
	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"export", "kubeconfig", "--name", "dev"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	cfg, err := clientcmd.Load(out.Bytes())
	if err != nil {
		t.Fatalf("clientcmd.Load: %v", err)
	}

	wantContext := "rask-dev"
	if cfg.CurrentContext != wantContext {
		t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, wantContext)
	}

	if _, ok := cfg.Contexts[wantContext]; !ok {
		t.Errorf("Contexts[%q] missing, got %v", wantContext, cfg.Contexts)
	}
}

func TestExportKubeconfig_KindCompatContextFormat(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	seedKubeconfig(t, homeDir, "dev")

	out := &bytes.Buffer{}
	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"export", "kubeconfig", "--name", "dev", "--context-format", "kind-{name}"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	cfg, err := clientcmd.Load(out.Bytes())
	if err != nil {
		t.Fatalf("clientcmd.Load: %v", err)
	}

	wantContext := "kind-dev"
	if cfg.CurrentContext != wantContext {
		t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, wantContext)
	}
}

func TestExportKubeconfig_OutputFlagWritesFile(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	seedKubeconfig(t, homeDir, "dev")

	outputPath := filepath.Join(t.TempDir(), "kubeconfig")

	root := newRootCommand(&fakeRuntime{}, homeDir)
	root.SetArgs([]string{"export", "kubeconfig", "--name", "dev", "--output", outputPath})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", outputPath, err)
	}

	if _, err := clientcmd.Load(data); err != nil {
		t.Errorf("clientcmd.Load(output file contents): %v", err)
	}
}
