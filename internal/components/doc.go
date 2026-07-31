// Package components downloads, verifies and caches the third-party
// binaries a rask cluster boots on top of (the core Kubernetes components,
// kine, containerd, runc and the CNI plugins). Downloads are cached under a
// version- and architecture-scoped directory (~/.rask/cache by default) so
// only the first "rask create" (or an explicit "rask pull") pays network
// cost.
//
// A binary built with `make bundle` (see internal/components/bundlepayload
// and cmd/bundle-payload) pays no network cost at all for its first
// "rask create" either: DefaultCache transparently resolves every fetch
// from an embedded payload instead, with no other change in behavior. See
// DefaultCache's doc comment for how a caller opts into this.
package components
