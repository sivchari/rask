// Package cluster holds identity constants shared by every rask cluster
// instance. These values are fixed across clusters (rather than randomized
// per create) so that a future memory/disk snapshot of a bootstrapped
// control plane can be restored without regenerating certificates or
// component configuration.
package cluster

// Fixed identity values for every rask cluster. Keeping these constant
// (instead of per-cluster) is what makes prebaked datastore snapshots and
// pre-generated PKI possible: the control plane's identity never changes,
// only which disk image or state directory backs it.
const (
	// NodeName is the Kubernetes node name used by the single node every
	// rask cluster runs.
	NodeName = "rask-node"

	// ServiceCIDR is the cluster IP range for Services.
	ServiceCIDR = "10.96.0.0/16"

	// APIServerServiceIP is the ClusterIP assigned to the kubernetes.default
	// Service, i.e. the first address in ServiceCIDR.
	APIServerServiceIP = "10.96.0.1"

	// DNSServiceIP is the fixed ClusterIP assigned to CoreDNS's Service
	// (kube-dns), the conventional tenth address in ServiceCIDR. kubelet
	// needs this address before CoreDNS itself necessarily exists yet
	// (it populates every pod's /etc/resolv.conf), so it is a cluster
	// identity constant like the others here rather than something
	// internal/manifests decides on its own.
	DNSServiceIP = "10.96.0.10"

	// PodCIDR is the cluster IP range for Pods.
	PodCIDR = "10.244.0.0/16"

	// Domain is the cluster's internal DNS domain.
	Domain = "cluster.local"
)
