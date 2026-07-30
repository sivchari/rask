package main

import (
	"testing"
	"time"
)

func TestParseStringField(t *testing.T) {
	tests := []struct {
		name string
		line string
		key  string
		want string
	}{
		{
			name: "field in the middle",
			line: "RASK-S3-NET-UP t=123ms iface=eth0 ip=192.168.64.5 gw=192.168.64.1 dns=[]",
			key:  "iface=",
			want: "eth0",
		},
		{
			name: "field at the end",
			line: "RASK-S3-RUN-DONE t=1.2s uname=x86_64",
			key:  "uname=",
			want: "x86_64",
		},
		{
			name: "key not present",
			line: "RASK-S3-MOUNTS-DONE t=5ms",
			key:  "uname=",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStringField(tt.line, tt.key)
			if got != tt.want {
				t.Errorf("parseStringField(%q, %q) = %q, want %q", tt.line, tt.key, got, tt.want)
			}
		})
	}
}

func TestParseDurationField(t *testing.T) {
	line := "RASK-S3-PULL-DONE t=4.532s"
	got := parseDurationField(line, "t=")
	want := 4532 * time.Millisecond
	if got != want {
		t.Errorf("parseDurationField(%q, %q) = %s, want %s", line, "t=", got, want)
	}
}

func TestParseMarker(t *testing.T) {
	var r runResult
	lines := []string{
		"RASK-S3-MOUNTS-DONE t=12ms",
		"RASK-S3-ROSETTA-MOUNTED",
		"RASK-S3-BINFMT-REGISTERED",
		"RASK-S3-NET-UP t=340ms iface=eth0 ip=192.168.64.5 gw=192.168.64.1 dns=[192.168.64.1]",
		"RASK-S3-CONTAINERD-READY t=210ms",
		"RASK-S3-PULL-DONE t=3.1s",
		"RASK-S3-RUN-DONE t=95ms uname=x86_64",
	}
	for _, l := range lines {
		parseMarker(l, &r)
	}

	if !r.rosettaMount {
		t.Error("rosettaMount = false, want true")
	}
	if !r.binfmtOK {
		t.Error("binfmtOK = false, want true")
	}
	if r.mounts != 12*time.Millisecond {
		t.Errorf("mounts = %s, want 12ms", r.mounts)
	}
	if r.netUp != 340*time.Millisecond {
		t.Errorf("netUp = %s, want 340ms", r.netUp)
	}
	if r.containerd != 210*time.Millisecond {
		t.Errorf("containerd = %s, want 210ms", r.containerd)
	}
	if r.pull != 3100*time.Millisecond {
		t.Errorf("pull = %s, want 3.1s", r.pull)
	}
	if r.run != 95*time.Millisecond {
		t.Errorf("run = %s, want 95ms", r.run)
	}
	if r.unameOutput != "x86_64" {
		t.Errorf("unameOutput = %q, want x86_64", r.unameOutput)
	}
}
