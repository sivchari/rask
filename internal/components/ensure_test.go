package components

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// slowFailingTransport delays every round trip by delay, then fails it (a
// 404) without ever needing a real network connection or a correct
// checksum fixture per component — proving ensureK8sBinaries/
// ensureNonK8sInto fetch their components concurrently only requires every
// independent download to take the same fixed delay, not that any of them
// actually succeed.
type slowFailingTransport struct {
	delay time.Duration
}

func (t *slowFailingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case <-time.After(t.delay):
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

// TestEnsure_FetchesComponentsConcurrentlyNotSerially guards against the
// regression this investigation found: Ensure resolves 6 k8s binaries plus
// kine/runc/containerd/cni-plugins — 10 independent downloads with no data
// dependency on one another — but did so one at a time. On a cold cache
// (the only time this touches the network at all — see Ensure's own doc
// comment) that serial chain was internal/substrate/vz's
// buildTemplateInitramfs's single largest contributor to a total cold-cache
// time measured close enough to vz.go's bootTimeout to produce a reported
// "hung" vz guest boot with no actual guest-side bug at all — the error
// surfaced depended only on which of Start's two waits had its shared
// deadline expire under it, not on anything actually being broken.
func TestEnsure_FetchesComponentsConcurrentlyNotSerially(t *testing.T) {
	t.Parallel()

	const delay = 150 * time.Millisecond

	c := NewCacheWithTransport(t.TempDir(), &slowFailingTransport{delay: delay})

	start := time.Now()

	_, err := c.Ensure(context.Background(), "v1.33.13", ARM64)

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ensure() against a transport that fails every request = nil error, want error")
	}

	// Serially, 10 independent delay-bound downloads would take >= 10 *
	// delay (1.5s here); concurrently, close to a single delay. 3*delay is
	// generous headroom over the concurrent case while still being far
	// short of the serial one, so this stays robust to scheduling jitter
	// without masking a real regression back to serial fetching.
	if want := 3 * delay; elapsed >= want {
		t.Errorf("Ensure() took %s to fail across 10 independent downloads at %s each — looks serial, not concurrent (want under %s)", elapsed, delay, want)
	}
}
