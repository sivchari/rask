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
