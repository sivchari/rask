// Package bootstrap launches the ordered set of processes that make up a
// rask cluster's control plane and node (datastore, API server, kubelet,
// container runtime, ...) and supervises them for the lifetime of the
// cluster. It is OS-agnostic: it only knows how to start, restart and stop
// os/exec commands, not how to build the command lines for a specific
// component.
package bootstrap
