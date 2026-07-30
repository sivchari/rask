package prebake

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKey_IncludesK8sVersionAndManifestDigest(t *testing.T) {
	t.Parallel()

	got := Key("v1.33.13")

	if !strings.HasPrefix(got, "v1.33.13-") {
		t.Errorf("Key(%q) = %q, want it to start with %q", "v1.33.13", got, "v1.33.13-")
	}

	digest := strings.TrimPrefix(got, "v1.33.13-")
	if len(digest) != 64 {
		t.Errorf("Key(%q) digest suffix = %q (len %d), want a 64-character hex sha256 digest", "v1.33.13", digest, len(digest))
	}
}

func TestKey_DiffersAcrossK8sVersions(t *testing.T) {
	t.Parallel()

	if Key("v1.33.13") == Key("v1.34.0") {
		t.Error("Key differs only by k8sVersion but produced the same value")
	}
}

func TestPath_JoinsHomeDirSeedsDirAndKey(t *testing.T) {
	t.Parallel()

	got := Path("/home/user/.rask", "v1.33.13")
	want := filepath.Join("/home/user/.rask", "seeds", Key("v1.33.13")+".db")

	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
