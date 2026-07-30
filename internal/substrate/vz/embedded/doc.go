//go:build darwin

// Package embedded holds the cross-compiled linux/arm64 cmd/rask-init
// binary, embedded into the rask executable at build time via go:embed so
// a released `rask` binary is self-contained: it never needs the source
// tree or a `go build` invocation available at runtime to produce the
// guest's PID 1.
//
// The rask-init binary itself (embedded/rask-init) is not committed as a
// real binary: `make build-rask-init` (see the repo Makefile) cross-compiles
// it fresh before every `make build`, overwriting the placeholder checked
// into version control. RaskInitBinary's doc comment explains how a caller
// detects the placeholder is still in place.
package embedded
