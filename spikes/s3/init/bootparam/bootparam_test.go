package bootparam

import "testing"

func TestParseBoottimeNanos(t *testing.T) {
	tests := []struct {
		name    string
		cmdline string
		wantN   int64
		wantOK  bool
		wantErr bool
	}{
		{
			name:    "present among other params",
			cmdline: "console=hvc0 reboot=t panic=-1 rask.boottime=1753789530123456789",
			wantN:   1753789530123456789,
			wantOK:  true,
		},
		{
			name:    "present alone",
			cmdline: "rask.boottime=42",
			wantN:   42,
			wantOK:  true,
		},
		{
			name:    "absent",
			cmdline: "console=hvc0 reboot=t panic=-1",
			wantOK:  false,
		},
		{
			name:    "empty cmdline",
			cmdline: "",
			wantOK:  false,
		},
		{
			name:    "malformed value",
			cmdline: "rask.boottime=notanumber",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok, err := ParseBoottimeNanos(tt.cmdline)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseBoottimeNanos(%q) error = %v, wantErr %v", tt.cmdline, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if ok != tt.wantOK {
				t.Errorf("ParseBoottimeNanos(%q) ok = %v, want %v", tt.cmdline, ok, tt.wantOK)
			}
			if ok && n != tt.wantN {
				t.Errorf("ParseBoottimeNanos(%q) = %d, want %d", tt.cmdline, n, tt.wantN)
			}
		})
	}
}
