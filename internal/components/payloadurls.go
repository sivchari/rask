package components

// PinnedURL is one network request rask's Cache may issue while resolving
// a (k8sVersion, arch) component set. Desc is a short human-readable label
// (component name, or "<name> checksum"), for the bundle-payload staging
// tool's logging only — it plays no role in matching at runtime, which is
// keyed purely by URL (see internal/components/bundlepayload's RoundTripper).
type PinnedURL struct {
	URL  string
	Desc string
}

// PinnedURLs returns every URL Cache may fetch to resolve k8sVersion/arch's
// component set: the six dl.k8s.io binaries (plus their checksum
// sidecars), kine, runc, containerd and cni-plugins. If includeGuest, it
// also includes every URL internal/substrate/vz's guest-kernel/busybox/
// iptables/gcompat/e2fsprogs/CA-bundle helpers need — the macOS vz
// substrate's guest userland, arm64-only regardless of arch.
//
// cmd/bundle-payload downloads every URL this returns and embeds the raw
// response bytes (see internal/components/bundlepayload), so a later
// DefaultCache (defaultcache.go) can resolve the exact same
// (k8sVersion, arch) pair fully offline. Building this list from the same
// unexported URL-builder
// functions Ensure/EnsureGuestKernel/etc. already call is deliberate: it
// guarantees the staged payload can never drift from what a real Cache
// would actually request.
func PinnedURLs(k8sVersion string, arch Arch, includeGuest bool) []PinnedURL {
	var urls []PinnedURL

	for _, bin := range k8sBinaries {
		urls = append(urls,
			PinnedURL{URL: k8sBinaryURL(k8sVersion, arch, bin), Desc: bin},
			PinnedURL{URL: k8sChecksumURL(k8sVersion, arch, bin), Desc: bin + " checksum"},
		)
	}

	urls = append(urls,
		PinnedURL{URL: kineURL(arch), Desc: "kine"},
		PinnedURL{URL: kineChecksumURL(arch), Desc: "kine checksum"},
		PinnedURL{URL: runcURL(arch), Desc: "runc"},
		PinnedURL{URL: runcChecksumURL(), Desc: "runc checksum"},
		PinnedURL{URL: containerdURL(arch), Desc: "containerd"},
		PinnedURL{URL: containerdChecksumURL(arch), Desc: "containerd checksum"},
		PinnedURL{URL: cniURL(arch), Desc: "cni-plugins"},
		PinnedURL{URL: cniChecksumURL(arch), Desc: "cni-plugins checksum"},
	)

	if !includeGuest {
		return urls
	}

	urls = append(urls, PinnedURL{URL: guestKernelURL(), Desc: "guest kernel"})
	urls = append(urls, PinnedURL{URL: busyboxPackage.url(), Desc: "busybox"})

	for _, p := range iptablesPackages {
		urls = append(urls, PinnedURL{URL: p.url(), Desc: "iptables:" + p.name})
	}

	for _, p := range gcompatPackages {
		urls = append(urls, PinnedURL{URL: p.url(), Desc: "gcompat:" + p.name})
	}

	for _, p := range e2fsprogsPackages {
		urls = append(urls, PinnedURL{URL: p.url(), Desc: "e2fsprogs:" + p.name})
	}

	urls = append(urls,
		PinnedURL{URL: caBundleURL, Desc: "ca-bundle"},
		PinnedURL{URL: caBundleChecksumURL, Desc: "ca-bundle checksum"},
	)

	return urls
}
