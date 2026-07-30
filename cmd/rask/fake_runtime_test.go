package main

import (
	"context"
	"errors"
	"io"

	"github.com/sivchari/rask/internal/substrate"
)

// fakeRuntime is a substrate.Runtime test double that records calls and
// returns configurable errors, so command wiring can be tested without a
// real substrate (VM or host process) implementation.
type fakeRuntime struct {
	createErr error
	startErr  error
	stopErr   error
	deleteErr error

	// onStart, if set, runs during Start after recording the call and
	// before startErr is returned — for tests simulating a real
	// substrate's Start-time side effects (e.g. hostproc writing a
	// timeline file), which must happen before "rask create" moves on
	// to its own post-Start steps.
	onStart func(name string) error

	createCalls    []string
	startCalls     []string
	startOptsCalls []substrate.StartOptions
	stopCalls      []string
	deleteCalls    []string
}

func (f *fakeRuntime) Create(_ context.Context, name string) error {
	f.createCalls = append(f.createCalls, name)

	return f.createErr
}

func (f *fakeRuntime) Start(_ context.Context, name string, opts substrate.StartOptions) error {
	f.startCalls = append(f.startCalls, name)
	f.startOptsCalls = append(f.startOptsCalls, opts)

	if f.onStart != nil {
		if err := f.onStart(name); err != nil {
			return err
		}
	}

	return f.startErr
}

func (f *fakeRuntime) Stop(_ context.Context, name string) error {
	f.stopCalls = append(f.stopCalls, name)

	return f.stopErr
}

func (f *fakeRuntime) Delete(_ context.Context, name string) error {
	f.deleteCalls = append(f.deleteCalls, name)

	return f.deleteErr
}

func (f *fakeRuntime) Exec(_ context.Context, _ string, _ io.Writer, _ string, _ ...string) (int, error) {
	return 0, errors.New("fakeRuntime: Exec not implemented")
}

func (f *fakeRuntime) WriteFile(_ context.Context, _ string, _ string, _ []byte) error {
	return errors.New("fakeRuntime: WriteFile not implemented")
}

func (f *fakeRuntime) PortForward(_ context.Context, _ string, _, _ string) (<-chan error, error) {
	return nil, errors.New("fakeRuntime: PortForward not implemented")
}
