package components

import "testing"

func TestPackageSetKey_IdenticalInputsReuseKey(t *testing.T) {
	t.Parallel()

	pkgs := []iptablesPackage{
		{name: "musl", version: "1.2.5-r11", sha256: "aaa"},
		{name: "iptables", version: "1.8.11-r1", sha256: "bbb"},
	}

	if got, want := packageSetKey(pkgs), packageSetKey(pkgs); got != want {
		t.Errorf("packageSetKey(pkgs) = %q, packageSetKey(pkgs) = %q, want equal", got, want)
	}
}

func TestPackageSetKey_ChangingAnyInputChangesKey(t *testing.T) {
	t.Parallel()

	base := []iptablesPackage{
		{name: "musl", version: "1.2.5-r11", sha256: "aaa"},
		{name: "iptables", version: "1.8.11-r1", sha256: "bbb"},
	}

	baseKey := packageSetKey(base)

	tests := []struct {
		name string
		pkgs []iptablesPackage
	}{
		{
			name: "different version",
			pkgs: []iptablesPackage{
				{name: "musl", version: "1.2.5-r12", sha256: "aaa"},
				{name: "iptables", version: "1.8.11-r1", sha256: "bbb"},
			},
		},
		{
			name: "different checksum",
			pkgs: []iptablesPackage{
				{name: "musl", version: "1.2.5-r11", sha256: "ccc"},
				{name: "iptables", version: "1.8.11-r1", sha256: "bbb"},
			},
		},
		{
			name: "extra package",
			pkgs: []iptablesPackage{
				{name: "musl", version: "1.2.5-r11", sha256: "aaa"},
				{name: "iptables", version: "1.8.11-r1", sha256: "bbb"},
				{name: "libmnl", version: "1.0.5-r2", sha256: "ddd"},
			},
		},
		{
			name: "fewer packages",
			pkgs: []iptablesPackage{
				{name: "musl", version: "1.2.5-r11", sha256: "aaa"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := packageSetKey(tt.pkgs); got == baseKey {
				t.Errorf("packageSetKey(%+v) = %q, want different from base key %q", tt.pkgs, got, baseKey)
			}
		})
	}
}
