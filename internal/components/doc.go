// Package components downloads, verifies and caches the third-party
// binaries a rask cluster boots on top of (the core Kubernetes components,
// kine, containerd, runc and the CNI plugins). Downloads are cached under a
// version- and architecture-scoped directory (~/.rask/cache by default) so
// only the first "rask create" (or an explicit "rask pull") pays network
// cost.
package components
