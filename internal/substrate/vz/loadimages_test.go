//go:build darwin

package vz_test

import (
	"context"
	"testing"

	"github.com/sivchari/rask/internal/substrate/vz"
)

func TestRuntime_LoadImages_ErrorWhenNotRunning(t *testing.T) {
	t.Parallel()

	r := vz.New(t.TempDir(), nil)

	if err := r.LoadImages(context.Background(), "never-started", nil); err == nil {
		t.Error("LoadImages on a never-started cluster = nil error, want error")
	}
}
