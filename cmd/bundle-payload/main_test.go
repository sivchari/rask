package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/components/bundlepayload"
)

func TestDownload_FetchesAndWritesFile(t *testing.T) {
	t.Parallel()

	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		_, _ = w.Write([]byte("payload-bytes"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "blob")

	if err := download(context.Background(), srv.Client(), srv.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}

	if string(got) != "payload-bytes" {
		t.Errorf("dest content = %q, want %q", got, "payload-bytes")
	}

	if requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestDownload_SkipsAlreadyStagedFile(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("download made a request for a file already present at dest")
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(dest, []byte("already-staged"), 0o644); err != nil {
		t.Fatalf("seeding dest: %v", err)
	}

	if err := download(context.Background(), srv.Client(), srv.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}

	if string(got) != "already-staged" {
		t.Errorf("dest content = %q, want unchanged %q", got, "already-staged")
	}
}

func TestDownload_NonOKStatusLeavesNoFile(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "blob")

	if err := download(context.Background(), srv.Client(), srv.URL, dest); err == nil {
		t.Fatal("download succeeded, want an error for a 404 response")
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest exists after a failed download: %v", err)
	}

	if _, err := os.Stat(dest + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a .tmp file was left behind after a failed download: %v", err)
	}
}

func TestWriteManifest_RoundTripsThroughBundlepayloadManifest(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "payload", "manifest.json")
	want := &bundlepayload.Manifest{
		OS:            "linux",
		Arch:          "amd64",
		K8sVersion:    "v1.33.13",
		IncludesGuest: false,
		URLs:          map[string]string{"https://example.invalid/kubectl": "payload/blobs/kubectl"},
	}

	if err := writeManifest(dest, want); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	var got bundlepayload.Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}

	if got.OS != want.OS || got.Arch != want.Arch || got.K8sVersion != want.K8sVersion || got.IncludesGuest != want.IncludesGuest {
		t.Errorf("manifest fields = %+v, want %+v", got, want)
	}

	if got.URLs["https://example.invalid/kubectl"] != "payload/blobs/kubectl" {
		t.Errorf("manifest URLs = %+v, want entry for kubectl", got.URLs)
	}
}

func TestPruneStalePayload_RemovesOtherTargetDebrisButKeepsGitkeepAndOwnFiles(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	payloadDir := filepath.Join(out, "payload")

	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		t.Fatalf("seeding payload dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(payloadDir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatalf("seeding .gitkeep: %v", err)
	}

	wantURL := components.PinnedURL{URL: "https://example.invalid/kubectl", Desc: "kubectl"}
	staleURL := components.PinnedURL{URL: "https://example.invalid/other-blob", Desc: "other-blob"}

	// Simulate a completed prior run (linux/amd64) having staged a blob it
	// owns plus a blob and an image this run's own -target (linux/arm64)
	// also wants — exactly what run() leaves behind before a second,
	// different -target invocation against the same out (goreleaser's
	// rask-bundled-amd64 then rask-bundled-arm64 hooks).
	// bundlepayload.BlobPath already returns a "payload/blobs/..." path
	// relative to out itself (see run()'s own blobRel/dest computation),
	// not relative to payloadDir.
	staleBlobDest := filepath.Join(out, filepath.FromSlash(bundlepayload.BlobPath(staleURL.URL)))
	if err := os.MkdirAll(filepath.Dir(staleBlobDest), 0o755); err != nil {
		t.Fatalf("seeding stale blob dir: %v", err)
	}

	if err := os.WriteFile(staleBlobDest, []byte("other-target-blob"), 0o644); err != nil {
		t.Fatalf("seeding stale blob: %v", err)
	}

	wantBlobDest := filepath.Join(out, filepath.FromSlash(bundlepayload.BlobPath(wantURL.URL)))
	if err := os.MkdirAll(filepath.Dir(wantBlobDest), 0o755); err != nil {
		t.Fatalf("seeding wanted blob dir: %v", err)
	}

	if err := os.WriteFile(wantBlobDest, []byte("already-staged-kubectl"), 0o644); err != nil {
		t.Fatalf("seeding wanted blob: %v", err)
	}

	staleImageDest := filepath.Join(payloadDir, "images", "amd64", "pause.tar")
	if err := os.MkdirAll(filepath.Dir(staleImageDest), 0o755); err != nil {
		t.Fatalf("seeding stale image dir: %v", err)
	}

	if err := os.WriteFile(staleImageDest, []byte("amd64-image"), 0o644); err != nil {
		t.Fatalf("seeding stale image: %v", err)
	}

	wantImageDest := filepath.Join(payloadDir, "images", "arm64", "pause.tar")
	if err := os.MkdirAll(filepath.Dir(wantImageDest), 0o755); err != nil {
		t.Fatalf("seeding wanted image dir: %v", err)
	}

	if err := os.WriteFile(wantImageDest, []byte("already-staged-arm64-image"), 0o644); err != nil {
		t.Fatalf("seeding wanted image: %v", err)
	}

	// The next invocation (linux/arm64, wanting only wantURL) prunes
	// out/payload down to its own desired set before staging anything,
	// exactly as run() does.
	if err := pruneStalePayload(out, components.ARM64, []components.PinnedURL{wantURL}); err != nil {
		t.Fatalf("pruneStalePayload: %v", err)
	}

	if _, err := os.Stat(staleBlobDest); !os.IsNotExist(err) {
		t.Errorf("other target's blob still present after pruneStalePayload: %v", err)
	}

	if _, err := os.Stat(staleImageDest); !os.IsNotExist(err) {
		t.Errorf("other target's image still present after pruneStalePayload: %v", err)
	}

	if got, err := os.ReadFile(wantBlobDest); err != nil || string(got) != "already-staged-kubectl" {
		t.Errorf("wanted blob = (%q, %v), want (%q, nil) — pruneStalePayload must leave it untouched", got, err, "already-staged-kubectl")
	}

	if got, err := os.ReadFile(wantImageDest); err != nil || string(got) != "already-staged-arm64-image" {
		t.Errorf("wanted image = (%q, %v), want (%q, nil) — pruneStalePayload must leave it untouched", got, err, "already-staged-arm64-image")
	}

	if _, err := os.Stat(filepath.Join(payloadDir, ".gitkeep")); err != nil {
		t.Errorf(".gitkeep missing after pruneStalePayload: %v", err)
	}
}

func TestPruneStalePayload_MissingPayloadDirIsNotAnError(t *testing.T) {
	t.Parallel()

	out := t.TempDir() // payload/ never created, as on a fresh checkout.

	if err := pruneStalePayload(out, components.AMD64, nil); err != nil {
		t.Fatalf("pruneStalePayload on a never-staged -out: %v", err)
	}
}

// TestPruneStalePayload_ThenDownloadSkipsNetwork exercises the same-target
// resume case end to end: run() calling pruneStalePayload followed by
// download() for a URL it already staged must not re-fetch it.
func TestPruneStalePayload_ThenDownloadSkipsNetwork(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("download made a request for a blob pruneStalePayload should have left in place")
	}))
	defer srv.Close()

	out := t.TempDir()
	url := components.PinnedURL{URL: srv.URL, Desc: "kubectl"}

	blobDest := filepath.Join(out, filepath.FromSlash(bundlepayload.BlobPath(url.URL)))
	if err := os.MkdirAll(filepath.Dir(blobDest), 0o755); err != nil {
		t.Fatalf("seeding blob dir: %v", err)
	}

	if err := os.WriteFile(blobDest, []byte("already-staged"), 0o644); err != nil {
		t.Fatalf("seeding blob: %v", err)
	}

	if err := pruneStalePayload(out, components.AMD64, []components.PinnedURL{url}); err != nil {
		t.Fatalf("pruneStalePayload: %v", err)
	}

	if err := download(context.Background(), srv.Client(), url.URL, blobDest); err != nil {
		t.Fatalf("download: %v", err)
	}

	got, err := os.ReadFile(blobDest)
	if err != nil || string(got) != "already-staged" {
		t.Errorf("blob content = (%q, %v), want (%q, nil)", got, err, "already-staged")
	}
}

func TestTargets_GuestOnlyForDarwinArm64(t *testing.T) {
	t.Parallel()

	for name, tgt := range targets {
		if tgt.guest && name != "darwin/arm64" {
			t.Errorf("target %q has guest=true, want guest only set for darwin/arm64", name)
		}
	}

	if !targets["darwin/arm64"].guest {
		t.Error(`targets["darwin/arm64"].guest = false, want true`)
	}
}
