// Package bundlepayload holds the go:embed payload `make bundle` compiles
// into a self-contained rask binary: raw HTTP response bytes for every
// internal/components.PinnedURL, plus prefetched container image archives,
// for one (Kubernetes version, arch, OS) target staged ahead of time by
// `make bundle-payload` (see cmd/bundle-payload).
//
// The committed internal/components/bundlepayload/payload directory is a
// placeholder (just .gitkeep): the real payload is gitignored and only
// exists in a working tree after `make bundle-payload` has run, exactly
// like internal/substrate/vz/embedded's committed-placeholder rask-init
// binary. This package's default build (no "bundle" build tag) embeds
// nothing at all — FS is the embed.FS zero value — so a plain `go build`
// or `go test` never depends on the payload directory's contents, and
// components.DefaultCache (internal/components/defaultcache.go)
// transparently falls back to real network fetches whenever Available
// reports false.
//
// Only `go build -tags bundle` (after `make bundle-payload` has staged a
// real payload) activates the real go:embed directive in fs_bundle.go.
package bundlepayload
