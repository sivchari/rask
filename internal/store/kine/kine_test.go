package kine_test

import (
	"context"
	"testing"

	"github.com/sivchari/rask/internal/store/kine"
)

func TestDatastore_MethodsReturnNotImplemented(t *testing.T) {
	t.Parallel()

	d := kine.New()
	ctx := context.Background()

	if _, err := d.Start(ctx); err == nil {
		t.Error("Start = nil error, want a not-implemented error")
	}

	if err := d.Stop(ctx); err == nil {
		t.Error("Stop = nil error, want a not-implemented error")
	}

	if err := d.SeedFrom(ctx, "/tmp/seed.db"); err == nil {
		t.Error("SeedFrom = nil error, want a not-implemented error")
	}
}
