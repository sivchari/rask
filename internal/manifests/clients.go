package manifests

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// BuildClients builds every client ApplyCoreDNS and ApplyLocalPathProvisioner
// need from a *rest.Config (as loaded from a cluster's kubeconfig). It is
// the one place in this package that talks to a real *rest.Config, so the
// apply functions themselves stay testable against fakes.
func BuildClients(config *rest.Config) (kubernetes.Interface, dynamic.Interface, meta.RESTMapper, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("manifests: building clientset: %w", err)
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("manifests: building dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("manifests: building discovery client: %w", err)
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	return clientset, dyn, mapper, nil
}
