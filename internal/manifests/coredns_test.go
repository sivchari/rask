package manifests

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/sivchari/rask/internal/cluster"
)

func TestApplyCoreDNS_CreatesEveryObject(t *testing.T) {
	t.Parallel()

	clientset := kubefake.NewSimpleClientset()
	ctx := context.Background()

	if err := ApplyCoreDNS(ctx, clientset); err != nil {
		t.Fatalf("ApplyCoreDNS: %v", err)
	}

	if _, err := clientset.CoreV1().ServiceAccounts(coreDNSNamespace).Get(ctx, "coredns", metav1.GetOptions{}); err != nil {
		t.Errorf("ServiceAccount not created: %v", err)
	}

	if _, err := clientset.RbacV1().ClusterRoles().Get(ctx, "system:coredns", metav1.GetOptions{}); err != nil {
		t.Errorf("ClusterRole not created: %v", err)
	}

	if _, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, "system:coredns", metav1.GetOptions{}); err != nil {
		t.Errorf("ClusterRoleBinding not created: %v", err)
	}

	cm, err := clientset.CoreV1().ConfigMaps(coreDNSNamespace).Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	if cm.Data["Corefile"] != corefile {
		t.Error("ConfigMap Corefile data does not match the rendered corefile")
	}

	dep, err := clientset.AppsV1().Deployments(coreDNSNamespace).Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}

	if dep.Spec.Template.Spec.Containers[0].Image != CoreDNSImage {
		t.Errorf("Deployment image = %q, want %q", dep.Spec.Template.Spec.Containers[0].Image, CoreDNSImage)
	}

	svc, err := clientset.CoreV1().Services(coreDNSNamespace).Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Service not created: %v", err)
	}

	if svc.Spec.ClusterIP != cluster.DNSServiceIP {
		t.Errorf("Service ClusterIP = %q, want %q (kubelet's KubeletConfiguration hardcodes clusterDNS to this address)", svc.Spec.ClusterIP, cluster.DNSServiceIP)
	}
}

func TestApplyCoreDNS_IdempotentOnReapply(t *testing.T) {
	t.Parallel()

	clientset := kubefake.NewSimpleClientset()
	ctx := context.Background()

	if err := ApplyCoreDNS(ctx, clientset); err != nil {
		t.Fatalf("first ApplyCoreDNS: %v", err)
	}

	if err := ApplyCoreDNS(ctx, clientset); err != nil {
		t.Fatalf("second ApplyCoreDNS (should be a no-op, not an error): %v", err)
	}
}

func TestCoreDNSDeployment_SelectorMatchesPodTemplateLabels(t *testing.T) {
	t.Parallel()

	dep := coreDNSDeployment()

	// A Deployment whose selector doesn't match its own pod template
	// never reports any Ready pods — this is the single most common way
	// to hand-write a Deployment that silently never becomes Ready.
	for k, want := range dep.Spec.Selector.MatchLabels {
		if got := dep.Spec.Template.Labels[k]; got != want {
			t.Errorf("pod template label %s = %q, want %q (must match Selector.MatchLabels)", k, got, want)
		}
	}
}

// TestCoreDNSDeployment_UsesDefaultDNSPolicy is a regression test: the
// default dnsPolicy (ClusterFirst) makes kubelet inject the cluster DNS
// Service IP as the pod's own nameserver — for the CoreDNS pod itself,
// that's its own Service IP, a literal self-loop CoreDNS's own loop
// detection correctly kills the pod for. Found via a real E2E run.
func TestCoreDNSDeployment_UsesDefaultDNSPolicy(t *testing.T) {
	t.Parallel()

	dep := coreDNSDeployment()

	if dep.Spec.Template.Spec.DNSPolicy != corev1.DNSDefault {
		t.Errorf("DNSPolicy = %q, want %q (ClusterFirst, the zero value, self-loops for CoreDNS's own pod)", dep.Spec.Template.Spec.DNSPolicy, corev1.DNSDefault)
	}
}

func TestCoreDNSClusterRoleBinding_ReferencesTheServiceAccountAndClusterRole(t *testing.T) {
	t.Parallel()

	sa := coreDNSServiceAccount()
	role := coreDNSClusterRole()
	binding := coreDNSClusterRoleBinding()

	if binding.RoleRef.Name != role.Name {
		t.Errorf("RoleRef.Name = %q, want %q", binding.RoleRef.Name, role.Name)
	}

	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != sa.Name || binding.Subjects[0].Namespace != sa.Namespace {
		t.Errorf("Subjects = %+v, want a single subject referencing %s/%s", binding.Subjects, sa.Namespace, sa.Name)
	}
}
