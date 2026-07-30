package bootstrap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
	"github.com/sivchari/rask/internal/components"
)

// fakeDatastore is a store.Datastore test double whose Start fails
// immediately, so Boot's error-propagation and fail-fast behavior can be
// exercised without a real kine binary or a full fake control plane (which
// would require a fake API server implementing client-go's watch
// protocol — out of scope for this unit test; the DAG's happy path is
// validated end-to-end against real binaries by test/e2e/linux.sh).
type fakeDatastore struct {
	startEndpoint string
	startErr      error

	stopCalls int
}

func (f *fakeDatastore) Start(context.Context) (string, error) { return f.startEndpoint, f.startErr }
func (f *fakeDatastore) Stop(context.Context) error {
	f.stopCalls++

	return nil
}
func (f *fakeDatastore) SeedFrom(context.Context, string) error { return nil }

func TestBoot_DatastoreFailurePropagatesAndFailsFast(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	wantErr := errors.New("datastore boom")

	cfg := bootstrap.Config{
		DataDir: dataDir,
		NodeIP:  "127.0.0.1",
		Paths: &components.Paths{
			// containerd is launched in parallel regardless of the
			// datastore's fate; point it at a real, harmless binary
			// (it will never open a unix socket, so its branch blocks
			// on waitUnixSocket until the errgroup's shared context is
			// canceled by the datastore branch's failure).
			Containerd: "/bin/sleep",
		},
		Datastore: &fakeDatastore{startErr: wantErr},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()

	_, err := bootstrap.Boot(ctx, cfg)
	if err == nil {
		t.Fatal("Boot() = nil error, want an error from the failing datastore")
	}

	if !errors.Is(err, wantErr) {
		t.Errorf("Boot() error = %v, want it to wrap %v", err, wantErr)
	}

	// The whole DAG must fail fast via errgroup's shared context
	// cancellation, not sit around waiting on containerd's socket that
	// will never appear.
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Boot() took %s to fail, want well under the 5s test timeout (DAG should fail fast)", elapsed)
	}
}

// TestBoot_DatastoreStoppedWhenLaterDAGStepFails is a regression test:
// cfg.Datastore runs outside bootstrap.Supervisor (see internal/store/kine),
// so a DAG failure that happens AFTER Datastore.Start already succeeded
// used to leak it as an orphaned process — Boot's failure path only called
// sup.Stop(), never cfg.Datastore.Stop(). Found via a real E2E run.
func TestBoot_DatastoreStoppedWhenLaterDAGStepFails(t *testing.T) {
	t.Parallel()

	ds := &fakeDatastore{startEndpoint: "unix:///nonexistent.sock"}

	cfg := bootstrap.Config{
		DataDir: t.TempDir(),
		NodeIP:  "127.0.0.1",
		Paths: &components.Paths{
			// Neither binary exists; apiserver's Launch will fail
			// (Datastore.Start already succeeded by then), which is
			// enough to reach Boot's failure path without needing a
			// real apiserver.
			KubeAPIServer: "/no/such/kube-apiserver-binary",
			Containerd:    "/bin/sleep",
		},
		Datastore: ds,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := bootstrap.Boot(ctx, cfg); err == nil {
		t.Fatal("Boot() = nil error, want an error from the missing apiserver binary")
	}

	if ds.stopCalls != 1 {
		t.Errorf("Datastore.Stop call count = %d, want 1 (Boot must stop the datastore on any DAG failure, not just its own)", ds.stopCalls)
	}
}
