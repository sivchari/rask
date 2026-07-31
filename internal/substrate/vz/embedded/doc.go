//go:build darwin

// Package embedded holds the cross-compiled linux/arm64 cmd/rask-init
// binary, embedded into the rask executable at build time via go:embed so
// a released `rask` binary is self-contained: it never needs the source
// tree or a `go build` invocation available at runtime to produce the
// guest's PID 1.
//
// The rask-init binary itself (embedded/rask-init) is gitignored rather than
// committed: `make build-rask-init` (see the repo Makefile) cross-compiles it
// fresh before every `make build`. It therefore does not exist in a fresh
// checkout, and this package does not compile until that target has run,
// which is why CI invokes it ahead of `go build`. RaskInitBinary's doc
// comment explains how a caller detects a file that is present but is not a
// real binary.
package embedded
