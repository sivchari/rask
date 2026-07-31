package manifests

import "testing"

func TestBundleDigest_DeterministicAndWellFormed(t *testing.T) {
	t.Parallel()

	got := BundleDigest(CoreDNSImage)

	if len(got) != 64 {
		t.Fatalf("BundleDigest() = %q (len %d), want a 64-character hex sha256 digest", got, len(got))
	}

	if again := BundleDigest(CoreDNSImage); again != got {
		t.Errorf("BundleDigest() is not deterministic: %q vs %q", got, again)
	}
}

// TestBundleDigest_SensitiveToLocalPathManifestContent guards against
// BundleDigest silently forgetting to cover a manifest source: if
// local-path-storage.yaml's content changes, the digest must change too, or
// a stale seed (internal/prebake) would keep matching a key it no longer
// actually represents.
func TestBundleDigest_SensitiveToLocalPathManifestContent(t *testing.T) {
	original := localPathStorageYAML
	t.Cleanup(func() { localPathStorageYAML = original })

	before := BundleDigest(CoreDNSImage)

	localPathStorageYAML = append([]byte(nil), original...)
	localPathStorageYAML = append(localPathStorageYAML, []byte("\n# test-only mutation\n")...)

	if after := BundleDigest(CoreDNSImage); after == before {
		t.Error("BundleDigest() unchanged after local-path-storage.yaml content changed")
	}
}

// TestBundleDigest_SensitiveToCoreDNSImage guards against a prebaked seed
// silently leaking the wrong CoreDNS image: internal/prebake's seed key
// must change whenever --coredns-image differs, or a seed built for one
// image would keep matching a request for a different one.
func TestBundleDigest_SensitiveToCoreDNSImage(t *testing.T) {
	t.Parallel()

	if BundleDigest(CoreDNSImage) == BundleDigest("registry.k8s.io/coredns/coredns:v1.11.1") {
		t.Error("BundleDigest() is the same for two different CoreDNS images")
	}
}
