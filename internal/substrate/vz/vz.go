//go:build darwin

// Package vz implements substrate.Runtime on top of macOS's
// Virtualization.framework (via code-hex/vz): one rask cluster is one
// lightweight Linux VM.
package vz

import (
	"context"
	"fmt"
	"io"

	"github.com/sivchari/rask/internal/substrate"
)

// Runtime implements substrate.Runtime using a Virtualization.framework VM
// per cluster.
type Runtime struct{}

var _ substrate.Runtime = (*Runtime)(nil)

// New returns a Runtime for the macOS vz substrate.
func New() *Runtime {
	return &Runtime{}
}

func (r *Runtime) Create(ctx context.Context, name string) error {
	// TODO(S2/M1): materialize the VM disk image (rootfs + prebaked
	// datastore) and boot configuration for the cluster.
	return fmt.Errorf("vz: Create(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Start(ctx context.Context, name string) error {
	return fmt.Errorf("vz: Start(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Stop(ctx context.Context, name string) error {
	return fmt.Errorf("vz: Stop(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Delete(ctx context.Context, name string) error {
	return fmt.Errorf("vz: Delete(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (int, error) {
	return 0, fmt.Errorf("vz: Exec(%s): not implemented on this platform yet", name)
}

func (r *Runtime) WriteFile(ctx context.Context, name string, path string, data []byte) error {
	return fmt.Errorf("vz: WriteFile(%s): not implemented on this platform yet", name)
}

func (r *Runtime) PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (<-chan error, error) {
	return nil, fmt.Errorf("vz: PortForward(%s): not implemented on this platform yet", name)
}
