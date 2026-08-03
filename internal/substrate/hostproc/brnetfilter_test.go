//go:build linux

package hostproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBridgeNetfilterFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// initial is the sysctl file's starting content; nil means the
		// file does not exist (module not loaded).
		initial []byte
		// loaderCreates is the content the injected module loader writes,
		// simulating a successful modprobe; nil simulates a loader that
		// cannot load the module (e.g. inside a container).
		loaderCreates []byte
		wantErr       string
		wantContent   string
	}{
		{
			name:        "already enabled",
			initial:     []byte("1\n"),
			wantContent: "1\n",
		},
		{
			name:        "disabled gets enabled",
			initial:     []byte("0\n"),
			wantContent: "1\n",
		},
		{
			name:          "missing but loader provides it",
			loaderCreates: []byte("1\n"),
			wantContent:   "1\n",
		},
		{
			name:          "missing, loader provides it disabled",
			loaderCreates: []byte("0\n"),
			wantContent:   "1\n",
		},
		{
			name:    "missing and loader cannot load",
			wantErr: "br_netfilter kernel module is not loaded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "bridge-nf-call-iptables")
			if tt.initial != nil {
				if err := os.WriteFile(path, tt.initial, 0o600); err != nil {
					t.Fatalf("seeding sysctl file: %v", err)
				}
			}

			loaderCalled := false
			loader := func() {
				loaderCalled = true

				if tt.loaderCreates != nil {
					if err := os.WriteFile(path, tt.loaderCreates, 0o600); err != nil {
						t.Fatalf("loader writing sysctl file: %v", err)
					}
				}
			}

			err := ensureBridgeNetfilterFile(path, loader)

			if tt.initial != nil && loaderCalled {
				t.Error("module loader ran even though the sysctl file already existed")
			}

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tt.wantErr)
				}

				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("ensureBridgeNetfilterFile: %v", err)
			}

			got, readErr := os.ReadFile(filepath.Clean(path))
			if readErr != nil {
				t.Fatalf("reading back sysctl file: %v", readErr)
			}

			if string(got) != tt.wantContent {
				t.Errorf("sysctl content = %q, want %q", got, tt.wantContent)
			}
		})
	}
}
