package manifests

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const applyTestManifest = `
apiVersion: v1
kind: Namespace
metadata:
  name: rask-test
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: rask-test-cm
  namespace: rask-test
data:
  key: value
`

// newTestDynamicClient returns a fake dynamic client and a matching static
// RESTMapper covering just the resource kinds this package's manifests use
// (Namespace and ConfigMap here; local-path-storage.yaml additionally uses
// ServiceAccount/Role/ClusterRole/RoleBinding/ClusterRoleBinding/Deployment/
// StorageClass, which apply.go resolves the same way against a real
// cluster's discovery-backed RESTMapper).
func newTestDynamicClient(t *testing.T) (*dynamicfake.FakeDynamicClient, meta.RESTMapper) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}: "NamespaceList",
		{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "", Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)

	return dyn, mapper
}

func TestApplyYAML_CreatesEveryDocument(t *testing.T) {
	t.Parallel()

	dyn, mapper := newTestDynamicClient(t)
	ctx := context.Background()

	if err := ApplyYAML(ctx, dyn, mapper, []byte(applyTestManifest)); err != nil {
		t.Fatalf("ApplyYAML: %v", err)
	}

	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	if _, err := dyn.Resource(nsGVR).Get(ctx, "rask-test", metav1.GetOptions{}); err != nil {
		t.Errorf("Namespace not created: %v", err)
	}

	cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	if _, err := dyn.Resource(cmGVR).Namespace("rask-test").Get(ctx, "rask-test-cm", metav1.GetOptions{}); err != nil {
		t.Errorf("ConfigMap not created: %v", err)
	}
}

func TestApplyYAML_IdempotentOnReapply(t *testing.T) {
	t.Parallel()

	dyn, mapper := newTestDynamicClient(t)
	ctx := context.Background()

	if err := ApplyYAML(ctx, dyn, mapper, []byte(applyTestManifest)); err != nil {
		t.Fatalf("first ApplyYAML: %v", err)
	}

	if err := ApplyYAML(ctx, dyn, mapper, []byte(applyTestManifest)); err != nil {
		t.Fatalf("second ApplyYAML (should be a no-op, not an error): %v", err)
	}
}

func TestApplyYAML_EmptyManifestIsANoOp(t *testing.T) {
	t.Parallel()

	dyn, mapper := newTestDynamicClient(t)

	if err := ApplyYAML(context.Background(), dyn, mapper, []byte("\n---\n\n")); err != nil {
		t.Errorf("ApplyYAML(empty) = %v, want nil", err)
	}
}

func TestApplyYAML_UnknownKindErrors(t *testing.T) {
	t.Parallel()

	dyn, mapper := newTestDynamicClient(t)

	manifest := []byte(`
apiVersion: nonexistent.example.com/v1
kind: TotallyMadeUp
metadata:
  name: whatever
`)

	if err := ApplyYAML(context.Background(), dyn, mapper, manifest); err == nil {
		t.Fatal("ApplyYAML with an unknown kind = nil error, want error")
	}
}
