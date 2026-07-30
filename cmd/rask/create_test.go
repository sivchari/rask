package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

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
