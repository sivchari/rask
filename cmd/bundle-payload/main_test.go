package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
