package cluster_test

import (
	"fmt"
	"path/filepath"

	"github.com/sivchari/rask/pkg/cluster"
)

// This example shows the shape fjord (an EKS emulator built on rask) is
// expected to drive rask through: pre-extracted EKS-D binaries instead of
// rask's own download cache, an authentication webhook config file placed
// into the cluster before boot, and the apiserver flag that points at it.
func Example_fjordIntegration() {
	provider, err := cluster.NewProvider("")
	if err != nil {
		fmt.Println("new provider:", err)

		return
	}

	// A preboot file's absolute destination is
	// <cluster-data-dir>/preboot/<dest> (see cluster.PrebootFile's doc
	// comment) — computable from the cluster's kubeconfig path, itself
	// available before Create ever runs.
	clusterDataDir := filepath.Join(filepath.Dir(provider.KubeConfigPath("haro")), "data")
	webhookDest := "auth/webhook.yaml"
	webhookAbsPath := filepath.Join(clusterDataDir, "preboot", webhookDest)

	opts := cluster.Options{
		Wait:         cluster.WaitCoreDNS,
		ComponentDir: "/var/lib/fjord/eksd/kubernetes-server/bin",
		ExtraAPIServerArgs: []string{
			"authentication-token-webhook-config-file=" + webhookAbsPath,
		},
		PrebootFiles: []cluster.PrebootFile{
			{Src: "/var/lib/fjord/authn/webhook-kubeconfig.yaml", Dest: webhookDest},
		},
		CoreDNSImage: "602401143452.dkr.ecr.us-west-2.amazonaws.com/eks/coredns:v1.11.4-eksbuild.2",
	}

	// A real caller boots the cluster here:
	//   result, err := provider.Create(ctx, "haro", opts)
	// Not called in this example: Create needs an actual host to create
	// real processes/VMs on, which a godoc example must not do as a side
	// effect of running "go test".

	fmt.Println(opts.ComponentDir)
	// Output: /var/lib/fjord/eksd/kubernetes-server/bin
}
