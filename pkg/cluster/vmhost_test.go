package cluster

import "testing"

func TestParseVMHostArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantOK   bool
		wantErr  bool
		wantHome string
		wantName string
	}{
		{
			name:   "no args beyond argv[0]",
			args:   []string{"rask"},
			wantOK: false,
		},
		{
			name:   "unrelated subcommand",
			args:   []string{"rask", "create"},
			wantOK: false,
		},
		{
			name:   "vm-host only as a later argument, not argv[1]",
			args:   []string{"rask", "create", "__vm-host"},
			wantOK: false,
		},
		{
			name:     "space-separated flags",
			args:     []string{"rask", "__vm-host", "--home", "/tmp/home", "--name", "dev"},
			wantOK:   true,
			wantHome: "/tmp/home",
			wantName: "dev",
		},
		{
			name:     "equals-separated flags",
			args:     []string{"rask", "__vm-host", "--home=/tmp/home", "--name=dev"},
			wantOK:   true,
			wantHome: "/tmp/home",
			wantName: "dev",
		},
		{
			name:     "flags in reverse order",
			args:     []string{"rask", "__vm-host", "--name", "dev", "--home", "/tmp/home"},
			wantOK:   true,
			wantHome: "/tmp/home",
			wantName: "dev",
		},
		{
			name:    "missing --home",
			args:    []string{"rask", "__vm-host", "--name", "dev"},
			wantOK:  true,
			wantErr: true,
		},
		{
			name:    "missing --name",
			args:    []string{"rask", "__vm-host", "--home", "/tmp/home"},
			wantOK:  true,
			wantErr: true,
		},
		{
			name:    "no flags at all",
			args:    []string{"rask", "__vm-host"},
			wantOK:  true,
			wantErr: true,
		},
		{
			name:    "malformed flag",
			args:    []string{"rask", "__vm-host", "--home"},
			wantOK:  true,
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"rask", "__vm-host", "--bogus", "x", "--home", "/tmp/home", "--name", "dev"},
			wantOK:  true,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home, name, ok, err := parseVMHostArgs(tt.args)

			if ok != tt.wantOK {
				t.Fatalf("parseVMHostArgs(%v) ok = %v, want %v", tt.args, ok, tt.wantOK)
			}

			if !tt.wantOK {
				if home != "" || name != "" || err != nil {
					t.Errorf("parseVMHostArgs(%v) = (%q, %q, %v, %v), want all zero when ok is false", tt.args, home, name, ok, err)
				}

				return
			}

			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVMHostArgs(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if home != tt.wantHome {
				t.Errorf("parseVMHostArgs(%v) home = %q, want %q", tt.args, home, tt.wantHome)
			}

			if name != tt.wantName {
				t.Errorf("parseVMHostArgs(%v) name = %q, want %q", tt.args, name, tt.wantName)
			}
		})
	}
}
