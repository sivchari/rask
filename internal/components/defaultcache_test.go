package components

import (
	"net/http"
	"testing"
)

// TestDefaultCache_SlimBuildMatchesNewCache pins DefaultCache's documented
// contract for the default (non-"bundle") build: bundlepayload.Available
// always reports false (see bundlepayload's own TestAvailable_SlimBuild),
// so DefaultCache must fall back to a plain Cache identical to what
// NewCache returns — no transport override, so http.DefaultTransport
// (rather than a bundlepayload.RoundTripper) ends up doing every fetch.
func TestDefaultCache_SlimBuildMatchesNewCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	c := DefaultCache(dir)

	if got := c.Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}

	if c.client.Transport != nil {
		t.Errorf("client.Transport = %#v, want nil (http.DefaultClient's zero-value transport) for a slim build", c.client.Transport)
	}

	plain := NewCache(dir)
	if c.client != http.DefaultClient || plain.client != http.DefaultClient {
		t.Error("DefaultCache and NewCache must both use http.DefaultClient when bundlepayload has nothing embedded")
	}
}
