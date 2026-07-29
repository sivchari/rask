//go:build darwin

package vz_test

import (
	"context"
	"testing"

	"github.com/sivchari/rask/internal/substrate/vz"
)

func TestRuntime_MethodsReturnNotImplemented(t *testing.T) {
	t.Parallel()

	r := vz.New()
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
	}{
		{name: "Create", call: func() error { return r.Create(ctx, "rask") }},
		{name: "Start", call: func() error { return r.Start(ctx, "rask") }},
		{name: "Stop", call: func() error { return r.Stop(ctx, "rask") }},
		{name: "Delete", call: func() error { return r.Delete(ctx, "rask") }},
		{name: "WriteFile", call: func() error { return r.WriteFile(ctx, "rask", "/etc/x", nil) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.call(); err == nil {
				t.Errorf("%s = nil error, want a not-implemented error", tt.name)
			}
		})
	}
}

func TestRuntime_ExecAndPortForwardReturnNotImplemented(t *testing.T) {
	t.Parallel()

	r := vz.New()
	ctx := context.Background()

	if _, err := r.Exec(ctx, "rask", nil, "true"); err == nil {
		t.Error("Exec = nil error, want a not-implemented error")
	}

	if _, err := r.PortForward(ctx, "rask", "127.0.0.1:0", "127.0.0.1:0"); err == nil {
		t.Error("PortForward = nil error, want a not-implemented error")
	}
}

func TestNew_ReturnsDistinctInstances(t *testing.T) {
	t.Parallel()

	a := vz.New()
	b := vz.New()

	if a == b {
		t.Error("New() returned the same instance twice, want independent instances")
	}
}
