//go:build darwin

package vz

import "testing"

// TestDisabledFeatures_AttachRosettaStillCompiles is a deliberate
// reference to disabledFeatures.attachRosetta: not a call (Rosetta stays
// fully inert, per the user's explicit direction after E2E testing
// correlated it with a real host crash — see attachRosetta's doc comment),
// just proof that the disabled code path still exists and type-checks for
// whenever it's revisited, rather than silently bit-rotting or getting
// flagged as dead code.
func TestDisabledFeatures_AttachRosettaStillCompiles(t *testing.T) {
	t.Parallel()

	if disabledFeatures.attachRosetta == nil {
		t.Fatal("disabledFeatures.attachRosetta is nil")
	}
}
