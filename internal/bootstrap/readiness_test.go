package bootstrap

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
