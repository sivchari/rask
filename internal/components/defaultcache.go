package components

import (
	"net/http"

	"github.com/sivchari/rask/internal/components/bundlepayload"
)

// DefaultCache returns the Cache every caller that doesn't need
// LocalDirSource's component-dir override should use: one backed by
// bundlepayload's embedded payload when `make bundle` staged and compiled
// one in (bundlepayload.Available reports true), else a plain
// network-backed Cache identical to what NewCache(dir) has always
// returned — so a default (slim) build's behavior is byte-for-byte
// unchanged.
//
// The embedded payload is wired in purely as an http.RoundTripper (see
// NewCacheWithTransport): every existing ComponentSource
// (DownloadCacheSource, LocalDirSource) and every Cache method
// (Ensure, EnsureGuestKernel, EnsureIPTablesBundle, ...) keeps resolving
// components exactly as before, just against a Cache whose fetches for a
// URL the payload captured are served from memory instead of the network.
//
// internal/substrate/hostproc and internal/substrate/vz both call this
// instead of NewCache directly for their default component cache.
func DefaultCache(dir string) *Cache {
	if !bundlepayload.Available() {
		return NewCache(dir)
	}

	return NewCacheWithTransport(dir, bundlepayload.RoundTripper(http.DefaultTransport))
}
