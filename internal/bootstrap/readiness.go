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
// is done.
func waitUnixSocket(ctx context.Context, path string) error {
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()

			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for unix socket %s: %w", path, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// waitHTTPOK polls url with client until it returns a 2xx status, or ctx is
// done.
func waitHTTPOK(ctx context.Context, client *http.Client, url string) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()

				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s to become healthy: %w", url, ctx.Err())
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
