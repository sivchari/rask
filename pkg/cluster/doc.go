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
package cluster
