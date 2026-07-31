//go:build bundle

package bundlepayload

import "embed"

// payloadFS is the real staged payload, embedded verbatim. `make
// bundle-payload` must have populated the payload directory before this
// compiles (an empty or missing directory is still a valid, if useless,
// embed target; Available reports false against it).
//
//go:embed all:payload
var payloadFS embed.FS
