package components

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// packageSetKey derives a short, deterministic, content-addressed
// identifier from a set of pinned Alpine packages (name, version and
// sha256 — see iptablesPackage), for use both as a bundle's own cache
// directory suffix and as an input to any dependent cache key (e.g.
// internal/substrate/vz/initramfs.go's template initramfs key).
//
// Exists to replace hand-maintained version constants (the old
// IPTablesBundleVersion/E2fsprogsBundleVersion/GCompatBundleVersion/
// BusyboxBundleVersion) that had to separately summarize a package list "by
// hand": IPTablesBundleVersion, for example, pinned only the "iptables"
// entry's own version while iptablesPackages' four other entries (musl,
// libmnl, libnftnl, libxtables) could drift without it ever noticing. A key
// derived directly from every package's actual (name, version, sha256)
// can't go stale that way — any change to any entry changes the key.
func packageSetKey(pkgs []iptablesPackage) string {
	h := sha256.New()

	for _, p := range pkgs {
		_, _ = fmt.Fprintf(h, "%s@%s:%s\n", p.name, p.version, p.sha256)
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}
