// Package kine implements store.Datastore by supervising a kine process
// (https://github.com/k3s-io/kine), which speaks the etcd gRPC API on top
// of an embedded SQLite database. This is rask's default datastore: it
// avoids running a real etcd for a single-node local cluster.
package kine

import (
	"context"
	"fmt"

	"github.com/sivchari/rask/internal/store"
)

// Datastore implements store.Datastore by supervising a kine process.
type Datastore struct{}

var _ store.Datastore = (*Datastore)(nil)

// New returns a kine-backed Datastore.
func New() *Datastore {
	return &Datastore{}
}

func (d *Datastore) Start(ctx context.Context) (string, error) {
	// TODO(M1): launch kine (via internal/bootstrap) against a SQLite
	// file in the cluster's state directory and return its listen
	// address.
	return "", fmt.Errorf("kine: Start: not implemented on this platform yet")
}

func (d *Datastore) Stop(ctx context.Context) error {
	return fmt.Errorf("kine: Stop: not implemented on this platform yet")
}

func (d *Datastore) SeedFrom(ctx context.Context, path string) error {
	// TODO(M1): copy the prebaked SQLite file from path into the
	// cluster's state directory before Start.
	return fmt.Errorf("kine: SeedFrom(%s): not implemented on this platform yet", path)
}
