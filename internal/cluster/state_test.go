package cluster_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/cluster"
)

func TestDir(t *testing.T) {
	t.Parallel()

	got := cluster.Dir("/home/user/.rask", "dev")
	want := filepath.Join("/home/user/.rask", "clusters", "dev")

	if got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T, homeDir string)
		want    []string
		wantErr bool
	}{
		{
			name:  "missing clusters directory yields empty list",
			setup: func(t *testing.T, homeDir string) {},
			want:  nil,
		},
		{
			name: "lists only directories, sorted",
			setup: func(t *testing.T, homeDir string) {
				t.Helper()

				mustMkdirAll(t, filepath.Join(homeDir, "clusters", "zeta"))
				mustMkdirAll(t, filepath.Join(homeDir, "clusters", "alpha"))
				mustMkdirAll(t, filepath.Join(homeDir, "clusters"))

				if err := os.WriteFile(filepath.Join(homeDir, "clusters", "not-a-cluster.txt"), []byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			},
			want: []string{"alpha", "zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			tt.setup(t, homeDir)

			got, err := cluster.List(homeDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("List() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !equalStrings(got, tt.want) {
				t.Errorf("List() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExists(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	mustMkdirAll(t, cluster.Dir(homeDir, "dev"))

	tests := []struct {
		name        string
		clusterName string
		want        bool
	}{
		{name: "existing cluster", clusterName: "dev", want: true},
		{name: "missing cluster", clusterName: "prod", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cluster.Exists(homeDir, tt.clusterName)
			if err != nil {
				t.Fatalf("Exists(%q): %v", tt.clusterName, err)
			}

			if got != tt.want {
				t.Errorf("Exists(%q) = %v, want %v", tt.clusterName, got, tt.want)
			}
		})
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func equalStrings(a, b []string) bool {
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
