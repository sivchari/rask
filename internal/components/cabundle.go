package components

import "context"

// caBundleURL is curl's well-known, continuously-refreshed mirror of
// Mozilla's CA certificate bundle (the same file curl/wget/git ship with
// on most distros), with a checksum sidecar in the same "hash  filename"
// shape ensureFile already parses for containerd/cni-plugins.
const (
	caBundleURL         = "https://curl.se/ca/cacert.pem"
	caBundleChecksumURL = caBundleURL + ".sha256"
)

// EnsureCABundle downloads (if not already cached) and verifies a CA
// certificate bundle for rask-init to install as
// /etc/ssl/certs/ca-certificates.crt inside the vz guest, so containerd's
// registry pulls (and any other guest-side TLS client) can verify public
// certificate chains. Unlike every other pinned component in this package,
// this file is not version-pinned: it's a continuously-updated root store,
// and rask always wants the current one rather than a stale pin, verified
// against curl.se's own checksum sidecar on every fetch (not against a
// version this package would need to keep bumping).
func (c *Cache) EnsureCABundle(ctx context.Context) (string, error) {
	return c.ensureFile(ctx, "ca-bundle", "cacert.pem", caBundleURL, caBundleChecksumURL, "cacert.pem")
}
