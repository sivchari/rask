package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

func TestGetClusters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, homeDir string)
		want  []string
	}{
		{
			name:  "no clusters",
			setup: func(t *testing.T, homeDir string) {},
			want:  []string{"No clusters found"},
		},
		{
			name: "sorted cluster names",
			setup: func(t *testing.T, homeDir string) {
				t.Helper()
				mustMkdirAll(t, cluster.Dir(homeDir, "zeta"))
				mustMkdirAll(t, cluster.Dir(homeDir, "alpha"))
			},
			want: []string{"alpha", "zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			tt.setup(t, homeDir)

			out := &bytes.Buffer{}
			root := newRootCommand(&fakeRuntime{}, homeDir)
			root.SetArgs([]string{"get", "clusters"})
			root.SetOut(out)
			root.SetErr(&bytes.Buffer{})

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute(): %v", err)
			}

			gotLines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if !equalStringSlices(gotLines, tt.want) {
				t.Errorf("output lines = %v, want %v", gotLines, tt.want)
			}
		})
	}
}

// TestGetPrebootPath deliberately never creates a "foo" cluster directory:
// PrebootPath is a pure computation callers (chicken-and-egg with "rask
// create cluster --apiserver-arg") must be able to run before the cluster
// exists, so this also proves that requirement.
func TestGetPrebootPath(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	rt := &fakeRuntime{}

	out := &bytes.Buffer{}
	root := newRootCommand(rt, homeDir)
	root.SetArgs([]string{"get", "preboot-path", "foo", "auth/webhook.yaml"})
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(): %v", err)
	}

	// Compare against fakeRuntime.PrebootPath's own formula rather than a
	// literal string, so this asserts what it's actually meant to: the
	// command passes args straight through to Provider.PrebootPath and
	// prints its return value verbatim, nothing more.
	want := rt.PrebootPath("foo", "auth/webhook.yaml") + "\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestGetPrebootPath_WrongArgCountRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no args", args: []string{"get", "preboot-path"}},
		{name: "one arg", args: []string{"get", "preboot-path", "foo"}},
		{name: "three args", args: []string{"get", "preboot-path", "foo", "auth/webhook.yaml", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newRootCommand(&fakeRuntime{}, t.TempDir())
			root.SetArgs(tt.args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			if err := root.Execute(); err == nil {
				t.Fatal("Execute() = nil error, want an error for the wrong argument count")
			}
		})
	}
}
