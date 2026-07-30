package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

func TestLoadDockerImage_MissingClusterRejectsWithoutTouchingDocker(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "docker-image", "busybox:1.36", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	// The cluster-existence check must reject before ever spawning "docker
	// save": otherwise this test would depend on a real Docker daemon.
	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want does-not-exist error")
	}

	if len(rt.loadImagesCalls) != 0 {
		t.Errorf("loadImagesCalls = %v, want no calls", rt.loadImagesCalls)
	}
}

func TestLoadDockerImage_NoImageArgsRejects(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "docker-image", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want an error for zero image arguments")
	}
}

func TestLoadImageArchive_MissingClusterRejectsWithoutOpeningFile(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", "/does/not/matter.tar", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want does-not-exist error")
	}

	if len(rt.loadImagesCalls) != 0 {
		t.Errorf("loadImagesCalls = %v, want no calls", rt.loadImagesCalls)
	}
}

func TestLoadImageArchive_MissingFileErrors(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", filepath.Join(t.TempDir(), "missing.tar"), "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want a file-not-found error")
	}
}

func TestLoadImageArchive_NoPathArgsRejects(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err == nil {
		t.Fatal("Execute() = nil error, want an error for zero path arguments")
	}
}

func TestLoadImageArchive_SuccessPassesEachArchiveToLoadImages(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	archive1 := filepath.Join(t.TempDir(), "one.tar")
	archive2 := filepath.Join(t.TempDir(), "two.tar")

	for _, p := range []string{archive1, archive2} {
		if err := os.WriteFile(p, []byte("fake tar contents"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", archive1, archive2, "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.loadImagesCalls) != 2 {
		t.Fatalf("loadImagesCalls = %+v, want exactly 2 calls (one per archive)", rt.loadImagesCalls)
	}

	for i, want := range []string{archive1, archive2} {
		call := rt.loadImagesCalls[i]

		if call.name != "dev" {
			t.Errorf("loadImagesCalls[%d].name = %q, want %q", i, call.name, "dev")
		}

		if len(call.references) != 1 || call.references[0] != want {
			t.Errorf("loadImagesCalls[%d].references = %v, want [%q]", i, call.references, want)
		}
	}
}

func TestLoadImageArchive_RuntimeFailurePropagatesAndStopsRemainingArchives(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	archive1 := filepath.Join(t.TempDir(), "one.tar")
	archive2 := filepath.Join(t.TempDir(), "two.tar")

	for _, p := range []string{archive1, archive2} {
		if err := os.WriteFile(p, []byte("fake tar contents"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}

	wantErr := errors.New("import boom")
	rt := &fakeRuntime{loadImagesErr: wantErr}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", archive1, archive2, "--name", "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want to wrap %v", err, wantErr)
	}

	if len(rt.loadImagesCalls) != 1 {
		t.Errorf("loadImagesCalls = %+v, want exactly 1 call (stops after the first failure)", rt.loadImagesCalls)
	}
}

func TestLoadImageArchive_DefaultNameFlag(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, defaultClusterName))

	archive := filepath.Join(t.TempDir(), "one.tar")
	if err := os.WriteFile(archive, []byte("fake tar contents"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", archive, err)
	}

	rt := &fakeRuntime{}

	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"load", "image-archive", archive})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	if len(rt.loadImagesCalls) != 1 || rt.loadImagesCalls[0].name != defaultClusterName {
		t.Errorf("loadImagesCalls = %+v, want exactly 1 call against cluster %q", rt.loadImagesCalls, defaultClusterName)
	}
}
