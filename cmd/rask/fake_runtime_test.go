package main

import (
	"context"
	"errors"
	"io"
)

// fakeRuntime is a substrate.Runtime test double that records calls and
// returns configurable errors, so command wiring can be tested without a
// real substrate (VM or host process) implementation.
type fakeRuntime struct {
	createErr error
	startErr  error
	stopErr   error
	deleteErr error

	createCalls []string
	startCalls  []string
	deleteCalls []string
}

func (f *fakeRuntime) Create(_ context.Context, name string) error {
	f.createCalls = append(f.createCalls, name)

	return f.createErr
}

func (f *fakeRuntime) Start(_ context.Context, name string) error {
	f.startCalls = append(f.startCalls, name)

	return f.startErr
}

func (f *fakeRuntime) Stop(_ context.Context, _ string) error {
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
