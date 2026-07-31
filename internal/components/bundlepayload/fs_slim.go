//go:build !bundle

package bundlepayload

import "embed"

// payloadFS is empty in the default (slim) build: only `go build -tags
// bundle` activates fs_bundle.go's real go:embed directive. The zero value
// of embed.FS is a valid, empty filesystem, so payload.go's manifest lookup
// (via ReadFile) always fails and Available reports false — every code
// path in this package degrades to a no-op / network-fallback.
var payloadFS embed.FS
