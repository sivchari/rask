package manifests

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWaitDeploymentReady_ReturnsOnceReadyReplicasPositive(t *testing.T) {
	t.Parallel()

	clientset := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := WaitDeploymentReady(ctx, clientset, "kube-system", "coredns"); err != nil {
		t.Errorf("WaitDeploymentReady() = %v, want nil", err)
	}
}

func TestWaitDeploymentReady_TimesOutWhenNeverReady(t *testing.T) {
	t.Parallel()

	clientset := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := WaitDeploymentReady(ctx, clientset, "kube-system", "coredns"); err == nil {
		t.Error("WaitDeploymentReady() = nil error, want a timeout error")
	}
}

func TestWaitDeploymentReady_TimesOutWhenDeploymentMissing(t *testing.T) {
	t.Parallel()

	clientset := fake.NewClientset()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := WaitDeploymentReady(ctx, clientset, "kube-system", "does-not-exist"); err == nil {
		t.Error("WaitDeploymentReady() = nil error, want a timeout error")
	}
}
