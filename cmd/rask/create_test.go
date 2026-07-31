package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/manifests"
	"github.com/sivchari/rask/internal/prebake"
	"github.com/sivchari/rask/internal/substrate"
)

func TestCreateCluster_UsesMatchingSeedWhenPresent(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	seedPath := prebake.Path(homeDir, components.DefaultK8sVersion, manifests.CoreDNSImage)
	mustMkdirAll(t, filepath.Dir(seedPath))

	if err := os.WriteFile(seedPath, []byte("fake seed sqlite contents"), 0o644); err != nil {
		t.Fatalf("writing fake seed file: %v", err)
	}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	if got := rt.startOptsCalls[0].SeedPath; got != seedPath {
		t.Errorf("StartOptions.SeedPath = %q, want %q", got, seedPath)
	}
}

func TestCreateCluster_NoMatchingSeedLeavesSeedPathEmpty(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	if got := rt.startOptsCalls[0].SeedPath; got != "" {
		t.Errorf("StartOptions.SeedPath = %q, want empty (no seed file present)", got)
	}
}

func TestCreateCluster_AlreadyExistsRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	rt := &fakeRuntime{}
	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want already-exists error")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want no calls", rt.createCalls)
	}
}

func TestCreateCluster_CreateFailurePropagatesAndLeavesNoState(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	wantErr := errors.New("not implemented on this platform yet")
	rt := &fakeRuntime{createErr: wantErr}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want to wrap %v", err, wantErr)
	}

	if got := []string{"dev"}; !equalStringSlices(rt.createCalls, got) {
		t.Errorf("createCalls = %v, want %v", rt.createCalls, got)
	}

	if len(rt.startCalls) != 0 {
		t.Errorf("startCalls = %v, want no calls (Create failed)", rt.startCalls)
	}

	exists, err := cluster.Exists(homeDir, "dev")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if exists {
		t.Error("cluster state directory was created despite Create failing")
	}
}

func TestCreateCluster_StartFailurePropagatesAndLeavesNoState(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	wantErr := errors.New("start boom")
	rt := &fakeRuntime{startErr: wantErr}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want to wrap %v", err, wantErr)
	}

	exists, err := cluster.Exists(homeDir, "dev")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if exists {
		t.Error("cluster state directory was created despite Start failing")
	}
}

func TestCreateCluster_SuccessCreatesStateDirectory(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	exists, err := cluster.Exists(homeDir, "dev")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Error("cluster state directory was not created after a successful create")
	}
}

func TestCreateCluster_DefaultNameFlag(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if got := []string{defaultClusterName}; !equalStringSlices(rt.createCalls, got) {
		t.Errorf("createCalls = %v, want %v", rt.createCalls, got)
	}
}

func TestCreateCluster_InvalidWaitValueRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--wait", "bogus"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want invalid --wait error")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want no calls", rt.createCalls)
	}
}

func TestCreateCluster_VerboseWithNoTimelineFilePrintsNothing(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	var out bytes.Buffer

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--verbose"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty (fakeRuntime never writes a timeline file)", out.String())
	}
}

func TestCreateCluster_VerbosePrintsTimelineFileWhenPresent(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// A real substrate (hostproc) writes cluster.Dir/data/timeline.txt
	// during Start, before "rask create" ever checks for it. Simulate
	// that exact ordering via onStart rather than pre-seeding the
	// directory before Execute(): createCluster's own cluster.Exists
	// precondition would otherwise reject a cluster whose directory
	// already exists before Create/Start ever ran.
	rt := &fakeRuntime{
		onStart: func(name string) error {
			dataDir := filepath.Join(cluster.Dir(homeDir, name), "data")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				return err
			}

			return os.WriteFile(filepath.Join(dataDir, "timeline.txt"), []byte("PHASE example\n"), 0o644)
		},
	}

	var out bytes.Buffer

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--verbose"})
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if !strings.Contains(out.String(), "PHASE example") {
		t.Errorf("stdout = %q, want it to contain the timeline file's content", out.String())
	}
}

func TestCreateCluster_APIAudienceFlagPassedThroughToStart(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--api-audience", "haro", "--api-audience", "another-client"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	want := substrate.StartOptions{ExtraAPIAudiences: []string{"haro", "another-client"}}
	if got := rt.startOptsCalls[0]; !equalStringSlices(got.ExtraAPIAudiences, want.ExtraAPIAudiences) {
		t.Errorf("StartOptions.ExtraAPIAudiences = %v, want %v", got.ExtraAPIAudiences, want.ExtraAPIAudiences)
	}
}

func TestCreateCluster_NoAPIAudienceFlagPassesEmptySlice(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	if got := rt.startOptsCalls[0].ExtraAPIAudiences; len(got) != 0 {
		t.Errorf("StartOptions.ExtraAPIAudiences = %v, want empty", got)
	}
}

func TestCreateCluster_APIServerArgFlagPassedThroughToStart(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{
		"create", "cluster", "--name", "dev",
		"--apiserver-arg", "authentication-token-webhook-config-file=/tmp/webhook.yaml",
		"--apiserver-arg", "requestheader-allowed-names=",
	})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	want := []string{"authentication-token-webhook-config-file=/tmp/webhook.yaml", "requestheader-allowed-names="}
	if got := rt.startOptsCalls[0].ExtraAPIServerArgs; !equalStringSlices(got, want) {
		t.Errorf("StartOptions.ExtraAPIServerArgs = %v, want %v", got, want)
	}
}

func TestCreateCluster_PrebootFileFlagParsedAndPassedThroughToStart(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{
		"create", "cluster", "--name", "dev",
		"--preboot-file", "/tmp/webhook-kubeconfig.yaml=auth/webhook.yaml",
	})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	want := []substrate.PrebootFile{{Src: "/tmp/webhook-kubeconfig.yaml", Dest: "auth/webhook.yaml"}}
	if got := rt.startOptsCalls[0].PrebootFiles; len(got) != 1 || got[0] != want[0] {
		t.Errorf("StartOptions.PrebootFiles = %v, want %v", got, want)
	}
}

func TestCreateCluster_InvalidPrebootFileFlagRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--preboot-file", "no-equals-sign"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() with an invalid --preboot-file = nil error, want error")
	}

	if len(rt.createCalls) != 0 {
		t.Errorf("createCalls = %v, want no calls", rt.createCalls)
	}
}

func TestCreateCluster_ComponentDirFlagPassedThroughToCreateAndStart(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--component-dir", "/opt/eksd/bin"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.createOptsCalls) != 1 || rt.createOptsCalls[0].ComponentDir != "/opt/eksd/bin" {
		t.Errorf("createOptsCalls = %v, want ComponentDir = /opt/eksd/bin", rt.createOptsCalls)
	}

	if len(rt.startOptsCalls) != 1 || rt.startOptsCalls[0].ComponentDir != "/opt/eksd/bin" {
		t.Errorf("startOptsCalls = %v, want ComponentDir = /opt/eksd/bin", rt.startOptsCalls)
	}
}

func TestCreateCluster_CoreDNSImageFlagPassedThroughAndAffectsSeedLookup(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	const overrideImage = "123456789012.dkr.ecr.us-west-2.amazonaws.com/eks/coredns:v1.11.4-eksbuild.2"

	// A seed built for the default CoreDNS image must NOT be picked up
	// for a create request using a different --coredns-image: prebake.Key
	// incorporates the image, so this seed file's key won't match.
	defaultSeedPath := prebake.Path(homeDir, components.DefaultK8sVersion, manifests.CoreDNSImage)
	mustMkdirAll(t, filepath.Dir(defaultSeedPath))

	if err := os.WriteFile(defaultSeedPath, []byte("fake seed sqlite contents"), 0o644); err != nil {
		t.Fatalf("writing fake seed file: %v", err)
	}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"create", "cluster", "--name", "dev", "--coredns-image", overrideImage})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.startOptsCalls) != 1 {
		t.Fatalf("startOptsCalls = %v, want exactly 1 call", rt.startOptsCalls)
	}

	got := rt.startOptsCalls[0]
	if got.CoreDNSImage != overrideImage {
		t.Errorf("StartOptions.CoreDNSImage = %q, want %q", got.CoreDNSImage, overrideImage)
	}

	if got.SeedPath != "" {
		t.Errorf("StartOptions.SeedPath = %q, want empty (the only cached seed is for the default image)", got.SeedPath)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
