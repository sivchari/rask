package manifests

import (
	"reflect"
	"testing"
)

func TestImagesFromYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name:     "single image",
			manifest: "spec:\n  containers:\n    - name: app\n      image: busybox\n      imagePullPolicy: IfNotPresent\n",
			want:     []string{"busybox"},
		},
		{
			name: "multiple images, order preserved",
			manifest: "" +
				"containers:\n  - name: provisioner\n    image: rancher/local-path-provisioner:v0.0.31\n" +
				"---\n" +
				"containers:\n  - name: helper-pod\n    image: busybox\n",
			want: []string{"rancher/local-path-provisioner:v0.0.31", "busybox"},
		},
		{
			name:     "duplicate images deduplicated",
			manifest: "image: busybox\nimage: busybox\n",
			want:     []string{"busybox"},
		},
		{
			name:     "quoted image value",
			manifest: `image: "busybox:1.36"` + "\n",
			want:     []string{"busybox:1.36"},
		},
		{
			name:     "no images",
			manifest: "apiVersion: v1\nkind: ConfigMap\n",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := imagesFromYAML([]byte(tt.manifest))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("imagesFromYAML(%q) = %v, want %v", tt.manifest, got, tt.want)
			}
		})
	}
}

func TestRequiredImages_IncludesCoreDNSAndLocalPathImages(t *testing.T) {
	t.Parallel()

	got := RequiredImages(CoreDNSImage)

	if len(got) == 0 || got[0] != CoreDNSImage {
		t.Fatalf("RequiredImages(%q)[0] = %v, want coreDNSImage first", CoreDNSImage, got)
	}

	want := imagesFromYAML(localPathStorageYAML)
	if !reflect.DeepEqual(got[1:], want) {
		t.Errorf("RequiredImages() local-path images = %v, want %v", got[1:], want)
	}

	if len(want) == 0 {
		t.Fatal("imagesFromYAML(localPathStorageYAML) = empty, want at least the provisioner and helper-pod images")
	}
}

func TestRequiredImages_RespectsCoreDNSImageOverride(t *testing.T) {
	t.Parallel()

	const override = "example.com/coredns:custom"

	got := RequiredImages(override)
	if got[0] != override {
		t.Errorf("RequiredImages(%q)[0] = %q, want %q", override, got[0], override)
	}
}
