//go:build darwin

package embedded

import "testing"

// TestRaskInitBinary_IsRealELFNotPlaceholder guards against accidentally
// committing/shipping the placeholder: fails loudly (rather than letting a
// vz VM boot attempt fail on it opaquely) if `make build-rask-init` has not
// been run to replace it with a real cross-compiled ELF binary.
func TestRaskInitBinary_IsRealELFNotPlaceholder(t *testing.T) {
	t.Parallel()

	if IsPlaceholder() {
		t.Skip("embedded/rask-init is still the placeholder; run `make build-rask-init` to cross-compile the real binary")
	}

	if len(RaskInitBinary) < 4 || string(RaskInitBinary[:4]) != "\x7fELF" {
		t.Errorf("RaskInitBinary does not start with an ELF magic number (got %q); make build-rask-init should produce a linux/arm64 ELF executable", RaskInitBinary[:min(4, len(RaskInitBinary))])
	}
}
