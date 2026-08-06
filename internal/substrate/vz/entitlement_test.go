//go:build darwin

package vz

import (
	"strings"
	"testing"
)

func TestEntitlementError_Message(t *testing.T) {
	t.Parallel()

	err := entitlementError("/path/to/rask")

	want := "vz: this binary is not signed with the com.apple.security.virtualization entitlement, so Virtualization.framework cannot start a VM.\n" +
		"Sign it with: codesign --entitlements vz.entitlements -f -s - /path/to/rask\n" +
		"(or build via `make codesign`, which does this for you)"

	if got := err.Error(); got != want {
		t.Errorf("entitlementError message =\n%s\nwant\n%s", got, want)
	}
}

func TestCheckVirtualizationEntitlement(t *testing.T) {
	tests := []struct {
		name       string
		probe      int
		wantErr    bool
		wantSubstr string
	}{
		{
			name:    "present: no error",
			probe:   entitlementPresent,
			wantErr: false,
		},
		{
			name:       "absent: blocks with the fix instructions",
			probe:      entitlementAbsent,
			wantErr:    true,
			wantSubstr: "codesign --entitlements vz.entitlements -f -s -",
		},
		{
			name:    "unknown: fails open, no error",
			probe:   entitlementUnknown,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// entitlementProbe is a shared package variable, so this test
			// cannot run in parallel with itself or others touching it.
			orig := entitlementProbe
			t.Cleanup(func() { entitlementProbe = orig })

			entitlementProbe = func() int { return tt.probe }

			err := checkVirtualizationEntitlement()
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkVirtualizationEntitlement() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("checkVirtualizationEntitlement() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}
