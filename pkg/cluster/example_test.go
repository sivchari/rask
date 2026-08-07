package cluster_test

import (
	"fmt"

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

	// PrebootPath resolves a PrebootFile's Dest to the path an in-cluster
	// component must actually read it from, computable before Create ever
	// runs. Never hand-build this path (e.g. by joining onto
	// KubeConfigPath's directory): the formula differs by substrate — a
	// host path on hostproc, an in-guest one on vz — and PrebootPath is the
	// one place that stays correct for whichever substrate this Provider
	// ends up using.
	const clusterName = "haro"

	webhookDest := "auth/webhook.yaml"
	webhookPath := provider.PrebootPath(clusterName, webhookDest)

	opts := cluster.Options{
		Wait:         cluster.WaitCoreDNS,
		ComponentDir: "/var/lib/fjord/eksd/kubernetes-server/bin",
		ExtraAPIServerArgs: []string{
			"authentication-token-webhook-config-file=" + webhookPath,
		},
		PrebootFiles: []cluster.PrebootFile{
			{Src: "/var/lib/fjord/authn/webhook-kubeconfig.yaml", Dest: webhookDest},
		},
		CoreDNSImage: "602401143452.dkr.ecr.us-west-2.amazonaws.com/eks/coredns:v1.11.4-eksbuild.2",
	}

	// A real caller boots the cluster here:
	//   result, err := provider.Create(ctx, clusterName, opts)
	// Not called in this example: Create needs an actual host to create
	// real processes/VMs on, which a godoc example must not do as a side
	// effect of running "go test".

	// Once the cluster is up, an EKS access-entry implementation running
	// on the host can reach a Service/Pod inside it with PortForward —
	// necessary on macOS, where vz runs everything inside a Linux guest
	// VM the host cannot dial directly:
	//   boundAddr, errs, err := provider.PortForward(ctx, clusterName, "127.0.0.1:0", serviceClusterIP+":443")
	// Not called in this example, for the same reason Create above isn't.

	fmt.Println(opts.ComponentDir)
	// Output: /var/lib/fjord/eksd/kubernetes-server/bin
}

// ExampleRunVMHostIfRequested shows the one line a program hosting rask
// clusters on macOS must add as the very first line of main, before its
// own flag/subcommand parsing: each cluster's VM runs in a detached
// process, spawned by re-execing the currently running binary with a
// hidden entrypoint (see RunVMHostIfRequested's doc comment for why).
// Calling this unconditionally is what lets that re-exec land here instead
// of failing with no matching subcommand.
func ExampleRunVMHostIfRequested() {
	if handled, err := cluster.RunVMHostIfRequested(); handled {
		if err != nil {
			fmt.Println("vm-host:", err)
		}

		return
	}

	// A real caller falls through to its own normal startup here (this
	// process was not asked to host a VM).
	fmt.Println("not a vm-host invocation")
	// Output: not a vm-host invocation
}
