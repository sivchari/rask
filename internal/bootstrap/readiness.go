package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// pollInterval governs every readiness probe Boot performs.
const pollInterval = 20 * time.Millisecond

// waitUnixSocket polls until path exists and accepts a connection, or ctx
// is done. The timeout error includes the last dial error observed, not
// just ctx.Err(), so a caller can tell e.g. "no such file" (nothing ever
// listened) apart from any other failure mode.
func waitUnixSocket(ctx context.Context, path string) error {
	var lastErr error

	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()

			return nil
		}

		lastErr = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for unix socket %s: %w (last dial error: %v)", path, ctx.Err(), lastErr)
		case <-time.After(pollInterval):
		}
	}
}

// waitHTTPOK polls url with client until it returns a 2xx status, or ctx is
// done. The timeout error includes the last error/status observed, not
// just ctx.Err(), so a caller can tell e.g. "connection refused" (process
// never started listening) apart from "500" (listening but unhealthy).
func waitHTTPOK(ctx context.Context, client *http.Client, url string) error {
	var lastErr error

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
		} else if resp, err := client.Do(req); err != nil {
			// A request aborted because ctx is already done says nothing
			// the returned ctx.Err() doesn't; keeping the last
			// substantive error (say "unexpected status 503") is what
			// tells the caller why the endpoint never became healthy.
			if lastErr == nil || ctx.Err() == nil {
				lastErr = err
			}
		} else {
			status := resp.StatusCode
			_ = resp.Body.Close()

			if status >= 200 && status < 300 {
				return nil
			}

			lastErr = fmt.Errorf("unexpected status %d", status)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s to become healthy: %w (last error: %v)", url, ctx.Err(), lastErr)
		case <-time.After(pollInterval):
		}
	}
}

// waitClosed blocks until ch is closed or ctx is done.
func waitClosed(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
