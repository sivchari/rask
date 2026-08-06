// Command bundle-payload downloads every URL internal/components.PinnedURLs
// returns for one platform target and writes the raw response bytes plus a
// manifest.json into internal/components/bundlepayload/payload, in the
// exact on-disk layout bundlepayload.RoundTripper/Available expect (see
// bundlepayload.BlobPath/ManifestPath). It also fetches every
// imagebundle.RequiredImages() container image, arch-matched to the
// target, via internal/imagebundle's own Cache directly into
// payload/images — the same {arch}/{sanitizedRef}.tar layout
// bundlepayload.InstallImages later copies out of the embedded payload and
// internal/imagebundle.Cache.Lookup reads from, so no second on-disk
// convention exists for images. `go build -tags bundle` then embeds that
// whole directory verbatim (fs_bundle.go), giving the resulting rask binary
// fully offline component AND image resolution for this one target — see
// internal/components/defaultcache.go and internal/imagebundle.Cache for
// how a running rask picks each of them up.
//
// Usage:
//
//	go run ./cmd/bundle-payload -target linux/amd64|linux/arm64|darwin/arm64 [-k8s-version v1.33.13] [-out internal/components/bundlepayload]
//
// -target fixes the (arch, guest-userland) pair together rather than
// exposing them as independent flags: darwin/arm64 is the only target that
// needs internal/substrate/vz's guest-kernel/busybox/iptables/gcompat/
// e2fsprogs/CA-bundle userland (its Create/RunVMHost paths fetch it
// unconditionally), and it's arm64-only (internal/substrate/vz/embedded's
// rask-init is cross-compiled GOOS=linux GOARCH=arm64); linux/amd64 and
// linux/arm64 never touch it (internal/substrate/hostproc runs Kubernetes
// components directly as host processes).
//
// Re-running with the same -target and -out resumes: a blob already
// present on disk from a prior partial run is not re-downloaded.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/components/bundlepayload"
	"github.com/sivchari/rask/internal/imagebundle"
)

// target describes one bundle-payload platform: the OS the resulting rask
// binary runs on, the Arch of the Kubernetes components it stages, and
// whether it also needs the vz substrate's guest userland.
type target struct {
	os    string
	arch  components.Arch
	guest bool
}

// targets is rask's full bundled-build platform matrix (see the Makefile's
// bundle-payload-* targets), keyed by GOOS/GOARCH the way `go build`
// itself names them.
var targets = map[string]target{
	"linux/amd64":  {os: "linux", arch: components.AMD64, guest: false},
	"linux/arm64":  {os: "linux", arch: components.ARM64, guest: false},
	"darwin/arm64": {os: "darwin", arch: components.ARM64, guest: true},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bundle-payload:", err)
		os.Exit(1)
	}
}

func run() error {
	targetFlag := flag.String("target", "", "platform target: linux/amd64, linux/arm64 or darwin/arm64 (required)")
	k8sVersion := flag.String("k8s-version", components.DefaultK8sVersion, "Kubernetes version to stage")
	out := flag.String("out", "internal/components/bundlepayload", "bundlepayload package directory to stage payload/ under")
	flag.Parse()

	tgt, ok := targets[*targetFlag]
	if !ok {
		return fmt.Errorf("-target must be one of linux/amd64, linux/arm64, darwin/arm64; got %q", *targetFlag)
	}

	urls := components.PinnedURLs(*k8sVersion, tgt.arch, tgt.guest)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := &http.Client{}

	manifest := bundlepayload.Manifest{
		OS:            tgt.os,
		Arch:          string(tgt.arch),
		K8sVersion:    *k8sVersion,
		IncludesGuest: tgt.guest,
		URLs:          make(map[string]string, len(urls)),
	}

	for i, u := range urls {
		blobRel := bundlepayload.BlobPath(u.URL)
		dest := filepath.Join(*out, filepath.FromSlash(blobRel))

		fmt.Fprintf(os.Stderr, "[%d/%d] %s (%s)\n", i+1, len(urls), u.Desc, u.URL)

		if err := download(ctx, client, u.URL, dest); err != nil {
			return fmt.Errorf("staging %s (%s): %w", u.Desc, u.URL, err)
		}

		manifest.URLs[u.URL] = blobRel
	}

	manifestDest := filepath.Join(*out, filepath.FromSlash(bundlepayload.ManifestPath))
	if err := writeManifest(manifestDest, &manifest); err != nil {
		return err
	}

	images := imagebundle.RequiredImages()

	fmt.Fprintf(os.Stderr, "bundle-payload: staging %d container images for %s...\n", len(images), *targetFlag)

	imageCache := imagebundle.NewCache(filepath.Join(*out, "payload", "images"))
	if _, err := imageCache.EnsureAll(ctx, images, tgt.arch); err != nil {
		return fmt.Errorf("staging images for %s: %w", *targetFlag, err)
	}

	fmt.Fprintf(os.Stderr, "bundle-payload: staged %d URLs and %d images for %s (k8s %s, guest=%v) into %s\n", len(urls), len(images), *targetFlag, *k8sVersion, tgt.guest, *out)

	return nil
}

// download fetches url into dest, skipping the request entirely if dest
// already exists (a prior run staged it — see this command's doc comment
// on resuming) and writing via a same-directory temp file plus rename so a
// killed/interrupted run never leaves a truncated blob at dest.
func download(ctx context.Context, client *http.Client, url, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	tmp := dest + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)

		return fmt.Errorf("writing %s: %w", tmp, err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return os.Rename(tmp, dest)
}

func writeManifest(dest string, m *bundlepayload.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	return os.WriteFile(dest, data, 0o644)
}
