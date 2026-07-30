package kine

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeKineEnv, when set on the child process only (via Datastore.extraEnv,
// never this test binary's own environment — that would race with other
// parallel tests), re-execs this test binary as a stand-in for the real
// kine binary: it opens a unix socket at the --listen-address it was given
// so these tests can exercise Datastore's real process-lifecycle and
// readiness-polling logic without a real kine binary, then blocks until
// SIGTERM.
const fakeKineEnv = "RASK_KINE_TEST_FAKE=1"

// fakeKineHangEnv additionally makes the fake process never open its
// socket, to exercise Start's readiness-timeout path.
const fakeKineHangEnv = "RASK_KINE_TEST_FAKE_HANG=1"

func TestMain(m *testing.M) {
	if os.Getenv("RASK_KINE_TEST_FAKE") == "1" {
		runFakeKine()

		return
	}

	os.Exit(m.Run())
}

// runFakeKine mimics just enough of kine's CLI contract (accepting
// --listen-address/--endpoint/--metrics-bind-address, listening on a unix
// socket, exiting on SIGTERM) for this package's tests.
func runFakeKine() {
	listenAddress := flag.String("listen-address", "", "")

	flag.String("endpoint", "", "")
	flag.String("metrics-bind-address", "", "")
	flag.Parse()

	if os.Getenv("RASK_KINE_TEST_FAKE_HANG") != "1" {
		addr := (*listenAddress)[len("unix://"):]

		l, err := net.Listen("unix", addr)
		if err != nil {
			os.Exit(1)
		}
		defer func() { _ = l.Close() }()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	<-sigCh
	os.Exit(0)
}

func TestDatastore_StartReturnsReadyEndpoint(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	endpoint, err := d.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	if filepath.Base(endpoint) != "kine.sock" {
		t.Errorf("Start() endpoint = %q, want it to end in kine.sock", endpoint)
	}

	conn, err := net.Dial("unix", endpoint[len("unix://"):])
	if err != nil {
		t.Errorf("endpoint %q is not actually accepting connections: %v", endpoint, err)
	} else {
		_ = conn.Close()
	}
}

func TestDatastore_PIDBeforeStartIsFalse(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)

	if _, ok := d.PID(); ok {
		t.Error("PID() before Start: ok = true, want false")
	}
}

func TestDatastore_PIDAfterStartIsLive(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	pid, ok := d.PID()
	if !ok {
		t.Fatal("PID() after Start: ok = false, want true")
	}

	if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
		t.Errorf("PID() = %d does not refer to a live process: %v", pid, err)
	}
}

func TestDatastore_StartTwiceErrors(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	if _, err := d.Start(ctx); err == nil {
		t.Error("second Start() = nil error, want error")
	}
}

func TestDatastore_StartTimesOutWhenSocketNeverAppears(t *testing.T) {
	t.Parallel()

	d := newHangingTestDatastore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := d.Start(ctx); err == nil {
		t.Fatal("Start() = nil error, want a timeout error")
	}
}

func TestDatastore_StopIsIdempotentAndSafeBeforeStart(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)

	if err := d.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start = %v, want nil", err)
	}
}

func TestDatastore_SeedFromCopiesFileBeforeStart(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	d := &Datastore{binPath: fakeKineBinPath(t), dataDir: dataDir}

	seedPath := filepath.Join(t.TempDir(), "seed.db")
	if err := os.WriteFile(seedPath, []byte("prebaked sqlite contents"), 0o644); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	if err := d.SeedFrom(context.Background(), seedPath); err != nil {
		t.Fatalf("SeedFrom: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dataDir, "state.db"))
	if err != nil {
		t.Fatalf("reading seeded state.db: %v", err)
	}

	if string(got) != "prebaked sqlite contents" {
		t.Errorf("seeded state.db = %q, want %q", got, "prebaked sqlite contents")
	}
}

func TestDatastore_SeedFromAfterStartErrors(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	if err := d.SeedFrom(context.Background(), "/tmp/whatever.db"); err == nil {
		t.Error("SeedFrom after Start = nil error, want error")
	}
}

func TestDatastore_StopKillsProcess(t *testing.T) {
	t.Parallel()

	d := newTestDatastore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	endpoint, err := d.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := net.Dial("unix", endpoint[len("unix://"):]); err == nil {
		t.Error("socket still accepting connections after Stop")
	}
}

// newTestDatastore returns a Datastore that runs this test binary as a fake
// kine process (see runFakeKine).
func newTestDatastore(t *testing.T) *Datastore {
	t.Helper()

	return &Datastore{
		binPath:  fakeKineBinPath(t),
		dataDir:  shortTempDir(t),
		extraEnv: []string{fakeKineEnv},
	}
}

// newHangingTestDatastore is like newTestDatastore, but the fake kine
// process never opens its listen socket.
func newHangingTestDatastore(t *testing.T) *Datastore {
	t.Helper()

	return &Datastore{
		binPath:  fakeKineBinPath(t),
		dataDir:  shortTempDir(t),
		extraEnv: []string{fakeKineEnv, fakeKineHangEnv},
	}
}

func fakeKineBinPath(t *testing.T) string {
	t.Helper()

	path, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	return path
}

// shortTempDir returns a fresh temp directory outside of t.TempDir()'s
// per-subtest-name nesting. unix domain socket paths are capped at ~104
// bytes on darwin (sockaddr_un.sun_path), and t.TempDir() embeds the full
// (potentially long) subtest name, which some of this package's test names
// exceed once "kine.sock" is appended.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "rask-kine-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}
