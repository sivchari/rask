// Package store defines the interface rask uses for the Kubernetes
// datastore backing the API server. The default implementation is kine
// (internal/store/kine) fronting SQLite; a real etcd is an opt-in
// alternative for cases where kine's semantics are insufficient.
package store

import "context"

// Datastore starts, stops and seeds the storage backend an API server uses.
type Datastore interface {
	// Start launches the datastore and returns the endpoint (e.g. a
	// unix socket or TCP address) the API server should connect to.
	Start(ctx context.Context) (endpoint string, err error)

	// Stop shuts the datastore down.
	Stop(ctx context.Context) error

	// SeedFrom replaces the datastore's contents with a prebaked state
	// file at path, skipping cluster bootstrap. It must be called
	// before Start.
	SeedFrom(ctx context.Context, path string) error
}
