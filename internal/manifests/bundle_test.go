package manifests

import "testing"

func TestBundleDigest_DeterministicAndWellFormed(t *testing.T) {
	t.Parallel()

	got := BundleDigest()

	if len(got) != 64 {
		t.Fatalf("BundleDigest() = %q (len %d), want a 64-character hex sha256 digest", got, len(got))
	}

	if again := BundleDigest(); again != got {
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

	before := BundleDigest()

	localPathStorageYAML = append([]byte(nil), original...)
	localPathStorageYAML = append(localPathStorageYAML, []byte("\n# test-only mutation\n")...)

	if after := BundleDigest(); after == before {
		t.Error("BundleDigest() unchanged after local-path-storage.yaml content changed")
	}
}
