//go:build darwin

// Package embedded holds the cross-compiled linux/arm64 cmd/rask-init
// binary, embedded into the rask executable at build time via go:embed so
// a released `rask` binary is self-contained: it never needs the source
// tree or a `go build` invocation available at runtime to produce the
// guest's PID 1.
//
// The committed embedded/rask-init is a text placeholder, not the real
// binary: it exists so this package (and any consumer importing rask as a
// Go module, whose module cache is read-only) compiles without running
// `make build-rask-init` first. That target cross-compiles the real binary
// fresh before every `make build`, overwriting the placeholder in the
// working tree; never commit that overwrite (restore with
// `git checkout -- internal/substrate/vz/embedded/rask-init`).
// IsPlaceholder detects the placeholder at runtime.
//
// Callers that actually need a real rask-init binary (internal/substrate/vz's
// buildTemplateInitramfs) never read RaskInitBinary directly for that
// purpose — they call Resolve, which falls back through $RASK_INIT_BINARY,
// then RaskInitBinary if it isn't a placeholder, then a verified release
// download cached under a components.Cache, so a Go module consumer whose
// build never ran `make build-rask-init` (e.g. fjord) still boots a real
// guest as long as it depends on an exact tagged rask release. See
// Resolve's doc comment for the full order and its final, actionable error.
package embedded
