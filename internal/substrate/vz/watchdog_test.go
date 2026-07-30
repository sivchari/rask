//go:build darwin

package vz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/guestagent"
)

func testAgentPort(t *testing.T, handler http.Handler) int {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing test server port: %v", err)
	}

	return port
}

func TestRunBootWatchdog_DoesNotCancelWhenGuestBecomesHealthy(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := testAgentPort(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failed := make(chan struct{}, 1)

	runBootWatchdog(ctx, cancel, port, time.Second, failed)

	select {
	case <-failed:
		t.Error("watchdog reported failure even though the guest became healthy")
	default:
	}

	if ctx.Err() != nil {
		t.Error("watchdog canceled ctx even though the guest became healthy")
	}
}

func TestRunBootWatchdog_CancelsAndReportsFailureOnTimeout(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	port := testAgentPort(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failed := make(chan struct{}, 1)

	runBootWatchdog(ctx, cancel, port, 100*time.Millisecond, failed)

	select {
	case <-failed:
	default:
		t.Error("watchdog did not report failure after the guest never became healthy")
	}

	if ctx.Err() == nil {
		t.Error("watchdog did not cancel ctx after the guest never became healthy")
	}
}

func TestRunBootWatchdog_DoesNotReportFailureWhenCtxAlreadyCanceledExternally(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	port := testAgentPort(t, mux)

	// Simulates a normal Stop: the outer ctx is canceled for a reason
	// unrelated to the boot watchdog (e.g. SIGTERM), before the watchdog's
	// own timeout would fire. This must not be misreported as a boot
	// timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	failed := make(chan struct{}, 1)

	runBootWatchdog(ctx, cancel, port, time.Second, failed)

	select {
	case <-failed:
		t.Error("watchdog reported failure for an externally-canceled ctx, want no failure report")
	default:
	}
}
