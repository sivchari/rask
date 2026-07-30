package manifests

import (
	"context"
	_ "embed"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
)

//go:embed local-path-storage.yaml
var localPathStorageYAML []byte

// ApplyLocalPathProvisioner applies local-path-provisioner and a default
// StorageClass (see local-path-storage.yaml) using dyn and mapper.
func ApplyLocalPathProvisioner(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper) error {
	return ApplyYAML(ctx, dyn, mapper, localPathStorageYAML)
}
