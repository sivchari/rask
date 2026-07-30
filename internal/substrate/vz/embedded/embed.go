//go:build darwin

package embedded

import _ "embed"

// RaskInitBinary is the cross-compiled (GOOS=linux GOARCH=arm64
// CGO_ENABLED=0) cmd/rask-init executable, embedded verbatim.
//
//go:embed rask-init
var RaskInitBinary []byte

// placeholderMagic is what the committed placeholder file (not a real
// binary — see this package's doc comment) starts with, letting
// IsPlaceholder detect "make build-rask-init was never run" at Runtime
// construction time instead of failing opaquely deep inside a VM boot.
const placeholderMagic = "RASK-INIT-PLACEHOLDER"

// IsPlaceholder reports whether RaskInitBinary is still the committed
// placeholder rather than a real cross-compiled binary.
func IsPlaceholder() bool {
	return len(RaskInitBinary) < len(placeholderMagic) || string(RaskInitBinary[:len(placeholderMagic)]) == placeholderMagic
}
