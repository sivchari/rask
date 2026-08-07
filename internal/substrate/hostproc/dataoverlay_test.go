//go:build linux

package hostproc

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureDataDirNotOverlayfsType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fsType  int64
		wantErr string
	}{
		{
			name:   "non-overlayfs (e.g. ext4)",
			fsType: unix.EXT4_SUPER_MAGIC,
		},
		{
			name:   "non-overlayfs (e.g. tmpfs)",
			fsType: unix.TMPFS_MAGIC,
		},
		{
			name:    "overlayfs",
			fsType:  unix.OVERLAYFS_SUPER_MAGIC,
			wantErr: "overlayfs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// dataDir intentionally doesn't exist: Start calls this before
			// bootstrap.Boot has created anything under it (see
			// nearestExistingAncestor's doc comment).
			dataDir := filepath.Join(t.TempDir(), "clusters", "test", "data")

			fsType := func(string) (int64, error) {
				return tt.fsType, nil
			}

			err := ensureDataDirNotOverlayfsType(dataDir, fsType)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ensureDataDirNotOverlayfsType: %v", err)
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

func TestNearestExistingAncestor(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	nested := filepath.Join(base, "clusters", "test", "data")

	got, err := nearestExistingAncestor(nested)
	if err != nil {
		t.Fatalf("nearestExistingAncestor: %v", err)
	}

	if got != base {
		t.Errorf("nearestExistingAncestor(%s) = %s, want %s", nested, got, base)
	}
}

func TestNearestExistingAncestorAlreadyExists(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	got, err := nearestExistingAncestor(base)
	if err != nil {
		t.Fatalf("nearestExistingAncestor: %v", err)
	}

	if got != base {
		t.Errorf("nearestExistingAncestor(%s) = %s, want %s", base, got, base)
	}
}
