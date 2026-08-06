//go:build darwin

package vz

import (
	"context"
	"errors"
	"fmt"

	"github.com/sivchari/rask/internal/substrate"
)

// LoadImages is not yet implemented for the vz substrate.
//
// Unlike internal/substrate/hostproc, which dials the cluster's containerd
// socket directly (see hostproc's LoadImages doc comment), a vz cluster's
// containerd instance runs entirely inside the guest VM with no
// host-reachable socket: the same gap documented at vz.go's Start doc
// comment for Config.SeedPath (a host-readable path into the guest's own
// disk). Closing it needs a guest-side import path through rask-init's
// control agent — like Exec/WriteFile, and like PortForward's guest-side
// tunnel endpoint (see agentclient.go and guestagent.PathTunnel) — which
// does not exist yet for image import specifically; this returns a clear
// error instead of a half-working implementation.
func (r *Runtime) LoadImages(_ context.Context, name string, _ []substrate.ImageSource) error {
	if _, ok := r.readPID(name); !ok {
		return fmt.Errorf("vz: cluster %q is not running", name)
	}

	return errors.New("vz: LoadImages is not implemented yet for the vz substrate; see LoadImages's doc comment")
}
