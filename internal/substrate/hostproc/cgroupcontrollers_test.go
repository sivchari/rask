//go:build linux

package hostproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCgroupControllersDelegatedFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// exists is false to simulate a cgroup v1 host (or cgroup2 not
		// mounted at the conventional path), where cgroup.controllers
		// does not exist at all.
		exists  bool
		content string
		wantErr string
	}{
		{
			name:   "cgroup v1 or non-cgroup2 host: no cgroup.controllers file",
			exists: false,
		},
		{
			name:    "all required controllers delegated",
			exists:  true,
			content: "cpuset cpu io memory pids rdma hugetlb misc\n",
		},
		{
			name:    "docker default privileged delegation: memory withheld",
			exists:  true,
			content: "cpuset cpu pids\n",
			wantErr: "memory",
		},
		{
			name:    "nothing delegated",
			exists:  true,
			content: "\n",
			wantErr: "cpu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "cgroup.controllers")
			if tt.exists {
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("seeding cgroup.controllers: %v", err)
				}
			}

			err := ensureCgroupControllersDelegatedFile(path)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ensureCgroupControllersDelegatedFile: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
