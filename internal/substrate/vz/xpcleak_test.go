//go:build darwin

package vz

import "testing"

func TestParseVirtualizationXPCPids(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   []int
	}{
		{
			name:   "single matching process",
			output: "57589 /System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine\n",
			want:   []int{57589},
		},
		{
			name: "matching and non-matching processes mixed",
			output: "1 /sbin/launchd\n" +
				"412 /usr/libexec/colima\n" +
				"57589 /System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine\n" +
				"58200 /System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine\n",
			want: []int{57589, 58200},
		},
		{
			name:   "no matches",
			output: "1 /sbin/launchd\n412 /usr/libexec/colima\n",
			want:   nil,
		},
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "blank lines are skipped",
			output: "\n\n57589 com.apple.Virtualization.VirtualMachine\n\n",
			want:   []int{57589},
		},
		{
			name:   "malformed leading field is skipped, not fatal",
			output: "not-a-pid com.apple.Virtualization.VirtualMachine\n57589 com.apple.Virtualization.VirtualMachine\n",
			want:   []int{57589},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseVirtualizationXPCPids([]byte(tt.output))

			if !intSlicesEqual(got, tt.want) {
				t.Errorf("parseVirtualizationXPCPids(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLsofHasOpenPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		path   string
		want   bool
	}{
		{
			name: "exact match",
			output: "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"com.apple 57589 me    6u   REG   1,15 10737418240   46638896 /Users/me/.rask/clusters/dev/data/disk.img\n",
			path: "/Users/me/.rask/clusters/dev/data/disk.img",
			want: true,
		},
		{
			name: "different cluster's disk is not a match",
			output: "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"com.apple 57589 me    6u   REG   1,15 10737418240   46638896 /Users/me/.rask/clusters/other/data/disk.img\n",
			path: "/Users/me/.rask/clusters/dev/data/disk.img",
			want: false,
		},
		{
			name: "deleted-but-open file still matches",
			output: "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"com.apple 57589 me    6u   REG   1,15 10737418240   46638896 /Users/me/.rask/clusters/dev/data/disk.img (deleted)\n",
			path: "/Users/me/.rask/clusters/dev/data/disk.img",
			want: true,
		},
		{
			name:   "no open files at all",
			output: "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n",
			path:   "/Users/me/.rask/clusters/dev/data/disk.img",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			path:   "/Users/me/.rask/clusters/dev/data/disk.img",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lsofHasOpenPath([]byte(tt.output), tt.path)
			if got != tt.want {
				t.Errorf("lsofHasOpenPath(%q, %q) = %v, want %v", tt.output, tt.path, got, tt.want)
			}
		})
	}
}

func intSlicesEqual(a, b []int) bool {
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
