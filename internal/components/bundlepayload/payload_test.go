package bundlepayload

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeFallback is an http.RoundTripper that records whether it was invoked,
// standing in for the real network transport RoundTripper falls back to.
type fakeFallback struct {
	called bool
}

func (f *fakeFallback) RoundTrip(req *http.Request) (*http.Response, error) {
	f.called = true

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(http.NoBody),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func TestAvailable_SlimBuild(t *testing.T) {
	t.Parallel()

	// The default (non-"bundle") build embeds nothing, so a manifest never
	// loads and Available must always report false.
	if Available() {
		t.Error("Available() = true, want false for the default (slim) build")
	}
}

func TestRoundTripper_FallsThroughWhenUnavailable(t *testing.T) {
	t.Parallel()

	fallback := &fakeFallback{}
	rt := RoundTripper(fallback)

	req, err := http.NewRequest(http.MethodGet, "https://dl.k8s.io/v1.33.13/bin/linux/arm64/kubectl", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !fallback.called {
		t.Error("RoundTrip did not delegate to fallback for an unmapped URL")
	}
}

func TestRoundTripper_NilFallbackDefaultsToDefaultTransport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rt := RoundTripper(nil)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestInstallImages_NoOpWithoutPayload(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()

	if err := InstallImages(dest); err != nil {
		t.Fatalf("InstallImages: %v", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("InstallImages wrote %d entries into dest, want 0 (no images subtree in the slim build)", len(entries))
	}
}

func TestBlobPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "k8s binary URL",
			url:  "https://dl.k8s.io/v1.33.13/bin/linux/arm64/kubectl",
			want: filepath.ToSlash(filepath.Join("payload", "blobs", "https___dl.k8s.io_v1.33.13_bin_linux_arm64_kubectl")),
		},
		{
			name: "two distinct URLs never collide",
			url:  "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc.arm64",
			want: filepath.ToSlash(filepath.Join("payload", "blobs", "https___github.com_opencontainers_runc_releases_download_v1.5.1_runc.arm64")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := BlobPath(tt.url); got != tt.want {
				t.Errorf("BlobPath(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
