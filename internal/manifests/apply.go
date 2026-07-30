package manifests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

// ApplyYAML applies every document in a multi-document YAML manifest
// (documents separated by "---") using dyn and mapper to resolve each
// document's REST resource. Creating an object that already exists is not
// an error: re-applying the same manifest against an already-seeded
// cluster (see internal/prebake) is a no-op, not a failure.
func ApplyYAML(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, manifest []byte) error {
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)

	for {
		obj := &unstructured.Unstructured{}

		if err := decoder.Decode(obj); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return fmt.Errorf("manifests: decoding manifest: %w", err)
		}

		if len(obj.Object) == 0 {
			continue
		}

		if err := applyOne(ctx, dyn, mapper, obj); err != nil {
			return err
		}
	}
}

func applyOne(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("manifests: resolving REST mapping for %s: %w", gvk, err)
	}

	var resource dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}

		resource = dyn.Resource(mapping.Resource).Namespace(ns)
	} else {
		resource = dyn.Resource(mapping.Resource)
	}

	if _, err := resource.Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("manifests: creating %s %q: %w", gvk.Kind, obj.GetName(), err)
	}

	return nil
}
