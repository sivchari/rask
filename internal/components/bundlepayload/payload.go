package bundlepayload

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ManifestPath is where cmd/bundle-payload must write the staged payload's
// manifest, relative to the bundlepayload package directory (payloadFS's
// root is the embedded "payload" directory itself — see fs_bundle.go's
// embed directive — so this is also payloadFS's own manifest path).
// Exported for cmd/bundle-payload, alongside BlobPath, so both packages
// agree on the on-disk layout without duplicating it.
const ManifestPath = "payload/manifest.json"

// imagesDir is the staged container-image archive subtree InstallImages
// copies into a caller's image cache directory, mirroring
// internal/imagebundle.Cache's own {arch}/{sanitizedRef}.tar layout.
const imagesDir = "payload/images"

// Manifest describes one staged payload: the (OS, arch, Kubernetes
// version) target cmd/bundle-payload staged it for, and which URL each
// blob file under payload/blobs corresponds to. RoundTripper and Available
// read it; cmd/bundle-payload writes it.
type Manifest struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	K8sVersion    string `json:"k8sVersion"`
	IncludesGuest bool   `json:"includesGuest"`
	// URLs maps a pinned component URL (see internal/components.PinnedURLs)
	// to the payloadFS-relative path of its captured raw response bytes.
	URLs map[string]string `json:"urls"`
}

// loadManifest reads and parses ManifestPath from payloadFS, returning
// (nil, false) if it isn't present — the default slim build, or a "bundle"
// build before `make bundle-payload` has staged anything.
func loadManifest() (*Manifest, bool) {
	data, err := fs.ReadFile(payloadFS, ManifestPath)
	if err != nil {
		return nil, false
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}

	return &m, true
}

// Available reports whether this build was compiled with -tags bundle
// against a payload directory `make bundle-payload` actually staged (as
// opposed to the default empty embed, or an untouched placeholder). Callers
// use this to decide whether embedded resolution is worth attempting at
// all rather than unconditionally wiring up RoundTripper/InstallImages.
func Available() bool {
	m, ok := loadManifest()

	return ok && len(m.URLs) > 0
}

// RoundTripper returns an http.RoundTripper that serves a request whose URL
// matches a manifest entry from the embedded payload, and delegates every
// other request to fallback (http.DefaultTransport if fallback is nil).
//
// This is the entire mechanism by which components.DefaultCache
// (internal/components/defaultcache.go) avoids the network: it is used
// as the http.Client transport of a components.Cache, so every one of
// Cache's existing fetch/verify/extract code paths (ensureFile,
// ensureArchive, EnsureGuestKernel, EnsureIPTablesBundle, ...) run
// completely unmodified, believing they're talking to the real dl.k8s.io /
// GitHub / Alpine mirrors.
func RoundTripper(fallback http.RoundTripper) http.RoundTripper {
	if fallback == nil {
		fallback = http.DefaultTransport
	}

	return &roundTripper{fallback: fallback}
}

type roundTripper struct {
	fallback http.RoundTripper
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m, ok := loadManifest()
	if ok {
		if blobPath, found := m.URLs[req.URL.String()]; found {
			data, err := fs.ReadFile(payloadFS, blobPath)
			if err == nil {
				return &http.Response{
					Status:        "200 OK",
					StatusCode:    http.StatusOK,
					Proto:         "HTTP/1.1",
					ProtoMajor:    1,
					ProtoMinor:    1,
					Body:          io.NopCloser(bytes.NewReader(data)),
					ContentLength: int64(len(data)),
					Header:        http.Header{},
					Request:       req,
				}, nil
			}
		}
	}

	return rt.fallback.RoundTrip(req)
}

// InstallImages copies every prefetched container-image archive this
// payload has staged (under payload/images/{arch}/{ref}.tar — the exact
// layout internal/imagebundle.Cache reads via Lookup/Ensure) into destDir,
// skipping any file already present there. It is a no-op, returning nil,
// when the payload has no images subtree (the default slim build, or a
// target that never staged images).
//
// Unlike component binaries (see RoundTripper), container images go
// through internal/imagebundle's crane-based OCI registry client rather
// than components.Cache's plain http.Client, so there is no single
// RoundTripper seam to intercept: InstallImages instead pre-populates the
// exact on-disk cache entries imagebundle.Cache.Lookup checks for, so its
// Ensure/EnsureAll find every image already cached and never call crane at
// all.
func InstallImages(destDir string) error {
	if _, err := fs.Stat(payloadFS, imagesDir); err != nil {
		return nil
	}

	return fs.WalkDir(payloadFS, imagesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		rel := strings.TrimPrefix(p, imagesDir+"/")
		dest := filepath.Join(destDir, filepath.FromSlash(rel))

		if _, statErr := os.Stat(dest); statErr == nil {
			return nil
		}

		data, err := fs.ReadFile(payloadFS, p)
		if err != nil {
			return fmt.Errorf("bundlepayload: reading %s: %w", p, err)
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("bundlepayload: creating %s: %w", filepath.Dir(dest), err)
		}

		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("bundlepayload: writing %s: %w", dest, err)
		}

		return nil
	})
}

// BlobPath returns the payloadFS-relative path cmd/bundle-payload stores
// url's captured response bytes at: url itself, sanitized into a single
// path segment, which is trivially collision-free since every PinnedURL is
// already unique.
//
// Exported for cmd/bundle-payload, the only other package that needs to
// agree with RoundTripper on this layout.
func BlobPath(url string) string {
	return path.Join("payload", "blobs", sanitizeURL(url))
}

// sanitizeURL replaces every character in url that isn't safe in a single
// path segment with '_', keeping the result human-readable for debugging,
// the same approach internal/imagebundle.sanitizeRef uses for image refs.
func sanitizeURL(url string) string {
	var b strings.Builder

	for _, r := range url {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	return b.String()
}
