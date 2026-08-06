// Package cluster is rask's importable Go API for creating and managing
// disposable local Kubernetes clusters: the library form of the "rask
// create/delete/get/export" CLI commands, for a consumer (e.g. fjord) that
// wants to drive rask in-process instead of shelling out to the rask
// binary.
//
// Pre-1.0: this API may still change, including in breaking ways, across
// rask releases without a major version bump. Pin a specific commit or tag
// if stability matters to your consumer.
//
// Provider selects its concrete implementation (supervised host processes
// on Linux, a Virtualization.framework VM per cluster on macOS) at compile
// time via the same build tags cmd/rask itself uses
// (internal/substrate/hostproc, internal/substrate/vz); every exported type
// in this package works identically on both.
//
// # Using rask as a library on macOS
//
// On macOS, the consuming program's own binary — not rask — is what
// actually hosts each cluster's VM, so it must do three things (see the
// repository README's "Using rask as a library" section for the full walk
// through):
//
//  1. Call RunVMHostIfRequested as the very first line of main, before any
//     of the program's own flag/subcommand parsing. Each cluster's VM runs
//     in a detached process that outlives the call that created it,
//     spawned by re-execing the currently running binary; without this
//     call, that re-exec has no matching entrypoint, and Provider.Create
//     fails only after a boot timeout instead of ever getting the chance
//     to host the VM. Safe to call unconditionally — it returns
//     (false, nil) immediately for an ordinary invocation, and compiles to
//     a no-op on Linux.
//  2. Cross-compile cmd/rask-init for linux/arm64 at build time, embed it
//     with a //go:embed directive, and supply it to NewProvider via
//     WithRaskInit — a module consumer's own checkout of rask's embedded
//     rask-init binary is a placeholder, not a real one (it has to be, so
//     `go build` works from a read-only module cache). See WithRaskInit's
//     doc comment for the exact build command.
//  3. Codesign the consuming binary itself with the
//     com.apple.security.virtualization entitlement: it, not rask, hosts
//     the VM. Provider.Create already fails fast with fix instructions if
//     this is missing.
package cluster
