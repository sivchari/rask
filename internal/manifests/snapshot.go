package manifests

import (
	"context"
	_ "embed"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
)

// snapshotCRDsYAML concatenates the three snapshot.storage.k8s.io/v1 CRDs
// (VolumeSnapshotClass, VolumeSnapshotContent, VolumeSnapshot) vendored
// from
// https://github.com/kubernetes-csi/external-snapshotter/tree/v8.6.0/client/config/crd
// (v8.6.0 matches the github.com/kubernetes-csi/external-snapshotter/client/v8
// version the haro operator dogfooding target imports). The
// groupsnapshot.storage.k8s.io CRDs in that same directory are
// deliberately excluded: the operator's scheme only registers
// volumesnapshot/v1 types (see cmd/main.go), not VolumeGroupSnapshot.
//
//go:embed snapshot-crds.yaml
var snapshotCRDsYAML []byte

// ApplySnapshotCRDs installs the external-snapshotter CRDs using dyn and
// mapper. Opt-in via "rask addon install snapshot-crds", not applied by
// every cluster's Start.
func ApplySnapshotCRDs(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper) error {
	return ApplyYAML(ctx, dyn, mapper, snapshotCRDsYAML)
}
