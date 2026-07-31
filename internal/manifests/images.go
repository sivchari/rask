package manifests

import "strings"

// RequiredImages returns every container image rask's default manifest
// bundle (see BundleDigest) can end up pulling: coreDNSImage (the CoreDNS
// Deployment's image, pass CoreDNSImage for rask's own default) plus every
// image local-path-storage.yaml's Deployment and helper-pod template
// reference. internal/imagebundle uses this list to know what to prefetch,
// so that once cached, a cluster's containerd already has every image
// present before ApplyCoreDNS/ApplyLocalPathProvisioner's pods (and, for
// the helper-pod image, a later PVC's provisioning pod) are ever scheduled,
// instead of pulling it from a registry on that critical path.
func RequiredImages(coreDNSImage string) []string {
	return append([]string{coreDNSImage}, imagesFromYAML(localPathStorageYAML)...)
}

// imagesFromYAML returns every distinct value of a YAML "image:" mapping
// key found in manifest, in first-seen order. This is a plain line scan
// rather than a full YAML parse, deliberately: it lets RequiredImages
// re-derive its image list straight from the embedded manifest instead of
// hardcoding a second copy of each image string that could silently drift
// from local-path-storage.yaml's actual pinned versions.
func imagesFromYAML(manifest []byte) []string {
	var images []string

	seen := make(map[string]bool)

	for _, line := range strings.Split(string(manifest), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "image:")
		if !ok {
			continue
		}

		image := strings.Trim(strings.TrimSpace(rest), `"'`)
		if image == "" || seen[image] {
			continue
		}

		seen[image] = true

		images = append(images, image)
	}

	return images
}
