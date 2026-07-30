package manifests

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// waitPollInterval is how often WaitDeploymentReady polls a Deployment
// while waiting for it to report a ready replica.
const waitPollInterval = 200 * time.Millisecond

// WaitDeploymentReady polls the Deployment named name in namespace until it
// reports at least one Ready replica, or ctx is done.
func WaitDeploymentReady(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
	for {
		dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && dep.Status.ReadyReplicas > 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("manifests: timed out waiting for %s/%s to become Ready: %w", namespace, name, ctx.Err())
		case <-time.After(waitPollInterval):
		}
	}
}
