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
// IsPlaceholder detects the placeholder at runtime and refuses to boot a
// VM from it.
package embedded
