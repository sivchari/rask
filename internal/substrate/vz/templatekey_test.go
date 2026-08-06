//go:build darwin

package vz

import "testing"

// TestTemplateInitramfsKey_IdenticalInputsReuseKey proves
// templateInitramfsKey is a pure function of raskInitBinary (plus the
// package-level pinned inputs it also reads): calling it twice with the
// same bytes must produce the same key, so an already-built template
// initramfs is still reused on a warm cache exactly as before.
func TestTemplateInitramfsKey_IdenticalInputsReuseKey(t *testing.T) {
	t.Parallel()

	raskInitBinary := []byte("a fake rask-init binary")

	if got, want := templateInitramfsKey(raskInitBinary), templateInitramfsKey(raskInitBinary); got != want {
		t.Errorf("templateInitramfsKey(raskInitBinary) = %q, templateInitramfsKey(raskInitBinary) = %q, want equal", got, want)
	}
}

// TestTemplateInitramfsKey_DifferentRaskInitBytesChangeKey proves a change
// to the actual rask-init bytes that will become /init changes the key —
// this is the exact class of staleness the old hand-maintained
// templateInitramfsVersion constant could silently miss (a new rask-init
// build with no corresponding version bump).
func TestTemplateInitramfsKey_DifferentRaskInitBytesChangeKey(t *testing.T) {
	t.Parallel()

	a := templateInitramfsKey([]byte("rask-init build A"))
	b := templateInitramfsKey([]byte("rask-init build B"))

	if a == b {
		t.Errorf("templateInitramfsKey returned the same key %q for two different rask-init binaries", a)
	}
}

// TestTemplateInitramfsKey_StableAcrossEmptyInput guards against a
// degenerate implementation that panics or returns an empty string for
// input it has never seen (e.g. the placeholder's tiny byte count).
func TestTemplateInitramfsKey_StableAcrossEmptyInput(t *testing.T) {
	t.Parallel()

	key := templateInitramfsKey(nil)
	if key == "" {
		t.Error("templateInitramfsKey(nil) = \"\", want a non-empty key")
	}
}
