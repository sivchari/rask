//go:build linux

// Package hostproc implements substrate.Runtime by supervising Kubernetes
// components as plain host processes inside a dedicated network namespace,
// rather than inside a VM. It targets Linux, where kind/k3d normally run
// inside a container: rask skips the container layer entirely.
package hostproc

import (
	"context"
	"fmt"
	"io"

	"github.com/sivchari/rask/internal/substrate"
)

// Runtime implements substrate.Runtime using supervised host processes.
type Runtime struct{}

var _ substrate.Runtime = (*Runtime)(nil)

// New returns a Runtime for the Linux host-process substrate.
func New() *Runtime {
	return &Runtime{}
}

func (r *Runtime) Create(ctx context.Context, name string) error {
	// TODO(M1): allocate a network namespace, bridge/veth pair and state
	// directory for the cluster, then hand off to internal/bootstrap to
	// launch kine/apiserver/kubelet/containerd.
	return fmt.Errorf("hostproc: Create(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Start(ctx context.Context, name string) error {
	return fmt.Errorf("hostproc: Start(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Stop(ctx context.Context, name string) error {
	return fmt.Errorf("hostproc: Stop(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Delete(ctx context.Context, name string) error {
	return fmt.Errorf("hostproc: Delete(%s): not implemented on this platform yet", name)
}

func (r *Runtime) Exec(ctx context.Context, name string, stdout io.Writer, command string, args ...string) (int, error) {
	return 0, fmt.Errorf("hostproc: Exec(%s): not implemented on this platform yet", name)
}

func (r *Runtime) WriteFile(ctx context.Context, name string, path string, data []byte) error {
	return fmt.Errorf("hostproc: WriteFile(%s): not implemented on this platform yet", name)
}

func (r *Runtime) PortForward(ctx context.Context, name string, localAddr, remoteAddr string) (<-chan error, error) {
	return nil, fmt.Errorf("hostproc: PortForward(%s): not implemented on this platform yet", name)
}
