package bootstrap

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitUnixSocket_SucceedsOnceSocketAccepts(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("", "rask-bootstrap-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- waitUnixSocket(ctx, path) }()

	time.Sleep(30 * time.Millisecond)

	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	if err := <-done; err != nil {
		t.Errorf("waitUnixSocket = %v, want nil", err)
	}
}

func TestWaitUnixSocket_TimesOutWhenSocketNeverAppears(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := waitUnixSocket(ctx, filepath.Join(t.TempDir(), "never.sock")); err == nil {
		t.Error("waitUnixSocket = nil error, want timeout error")
	}
}

func TestWaitUnixSocket_TimeoutErrorIncludesLastDialError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := waitUnixSocket(ctx, filepath.Join(t.TempDir(), "never.sock"))
	if err == nil {
		t.Fatal("waitUnixSocket = nil error, want timeout error")
	}

	if !strings.Contains(err.Error(), "last dial error:") {
		t.Errorf("waitUnixSocket error = %q, want it to include the last dial error", err.Error())
	}
}

func TestWaitHTTPOK_SucceedsOn2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := waitHTTPOK(ctx, srv.Client(), srv.URL); err != nil {
		t.Errorf("waitHTTPOK = %v, want nil", err)
	}
}

func TestWaitHTTPOK_RetriesThroughNon2xxUntilReady(t *testing.T) {
	t.Parallel()

	failuresRemaining := 3

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failuresRemaining > 0 {
			failuresRemaining--
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := waitHTTPOK(ctx, srv.Client(), srv.URL); err != nil {
		t.Errorf("waitHTTPOK = %v, want nil", err)
	}

	if failuresRemaining != 0 {
		t.Errorf("failuresRemaining = %d, want 0 (waitHTTPOK returned before exhausting them)", failuresRemaining)
	}
}

func TestWaitHTTPOK_TimesOutWhenNeverReady(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := waitHTTPOK(ctx, srv.Client(), srv.URL); err == nil {
		t.Error("waitHTTPOK = nil error, want timeout error")
	}
}

func TestWaitHTTPOK_TimeoutErrorIncludesLastStatus(t *testing.T) {
	t.Parallel()

	// Cancellation is driven by requests actually served, not by a wall
	// clock: with a fixed short timeout, a loaded machine can cancel the
	// very first request in flight, leaving the context error — not the
	// 503 this asserts on — as the last error observed.
	served := make(chan struct{}, 2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)

		select {
		case served <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- waitHTTPOK(ctx, srv.Client(), srv.URL) }()

	// The second request proves the first response was fully consumed, so
	// the 503 is already recorded as the last error.
	<-served
	<-served

	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("waitHTTPOK = nil error, want cancellation error")
	}

	if !strings.Contains(err.Error(), "503") {
		t.Errorf("waitHTTPOK error = %q, want it to mention the last HTTP status (503)", err.Error())
	}
}

func TestWaitHTTPOK_TimeoutErrorIncludesLastDialError(t *testing.T) {
	t.Parallel()

	// Nothing listens here; every attempt fails at connect time, not with
	// a non-2xx status, so the timeout error should surface that dial
	// failure instead of a generic "context deadline exceeded" only.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := waitHTTPOK(ctx, http.DefaultClient, "http://127.0.0.1:1/")
	if err == nil {
		t.Fatal("waitHTTPOK = nil error, want timeout error")
	}

	if !strings.Contains(err.Error(), "last error:") {
		t.Errorf("waitHTTPOK error = %q, want it to include the last dial error", err.Error())
	}
}

func TestWaitClosed_ReturnsNilWhenChannelCloses(t *testing.T) {
	t.Parallel()

	ch := make(chan struct{})
	close(ch)

	if err := waitClosed(context.Background(), ch); err != nil {
		t.Errorf("waitClosed = %v, want nil", err)
	}
}

func TestWaitClosed_ReturnsCtxErrorWhenCtxDoneFirst(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := waitClosed(ctx, make(chan struct{})); err == nil {
		t.Error("waitClosed = nil error, want ctx deadline error")
	}
}
