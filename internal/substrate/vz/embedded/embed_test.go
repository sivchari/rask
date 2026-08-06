//go:build darwin

package embedded

import (
	"strings"
	"testing"
)

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

// TestIsPlaceholderBytes pins IsPlaceholder's detection logic against
// arbitrary byte slices (not just whatever RaskInitBinary happens to
// contain in this build environment), so this test always exercises the
// logic instead of skipping whenever `make build-rask-init` has run.
func TestIsPlaceholderBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"exact placeholder magic", []byte(placeholderMagic), true},
		{"placeholder magic with trailing content", []byte(placeholderMagic + "\nsee doc.go\n"), true},
		{"empty", nil, true},
		{"shorter than the magic", []byte("RASK-INIT"), true},
		{"real ELF magic", append([]byte("\x7fELF"), make([]byte, 64)...), false},
		{"same length as magic but different content", []byte(strings.Repeat("x", len(placeholderMagic))), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isPlaceholderBytes(tt.data); got != tt.want {
				t.Errorf("isPlaceholderBytes(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}
