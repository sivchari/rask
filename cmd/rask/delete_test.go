package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

func TestDeleteCluster_MissingClusterRejectsWithoutCallingRuntime(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"delete", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want does-not-exist error")
	}

	if len(rt.deleteCalls) != 0 {
		t.Errorf("deleteCalls = %v, want no calls", rt.deleteCalls)
	}
}

func TestDeleteCluster_RuntimeFailureLeavesStateDirectory(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	wantErr := errors.New("delete boom")
	rt := &fakeRuntime{deleteErr: wantErr}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"delete", "cluster", "--name", "dev"})
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

	if !exists {
		t.Error("cluster state directory was removed despite Delete failing")
	}
}

func TestDeleteCluster_StopFailurePreventsDeleteAndLeavesStateDirectory(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	wantErr := errors.New("stop boom")
	rt := &fakeRuntime{stopErr: wantErr}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"delete", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want to wrap %v", err, wantErr)
	}

	if len(rt.deleteCalls) != 0 {
		t.Errorf("deleteCalls = %v, want no calls (Stop failed first)", rt.deleteCalls)
	}

	exists, err := cluster.Exists(homeDir, "dev")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if !exists {
		t.Error("cluster state directory was removed despite Stop failing")
	}
}

func TestDeleteCluster_StopCalledBeforeDelete(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"delete", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if got := []string{"dev"}; !equalStringSlices(rt.stopCalls, got) {
		t.Errorf("stopCalls = %v, want %v", rt.stopCalls, got)
	}
}

func TestDeleteCluster_SuccessRemovesStateDirectory(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"delete", "cluster", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	exists, err := cluster.Exists(homeDir, "dev")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}

	if exists {
		t.Error("cluster state directory still exists after a successful delete")
	}

	if got := []string{"dev"}; !equalStringSlices(rt.deleteCalls, got) {
		t.Errorf("deleteCalls = %v, want %v", rt.deleteCalls, got)
	}
}
