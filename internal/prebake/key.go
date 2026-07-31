// Package prebake builds and locates prebaked cluster-state snapshots
// ("seeds"): a captured kine/SQLite datastore file already containing the
// RBAC bootstrap ClusterRoles/ClusterRoleBindings, default namespaces, and
// default manifest bundle (CoreDNS, local-path-provisioner) that a cold
// "rask create" would otherwise reconcile/apply from scratch. See
// spikes/s1/RESULTS.md for why this shaves time off apiserver's own
// bootstrap ("apiserver_readyz"), the dominant cost in a cold create.
package prebake

import (
	"path/filepath"

	"github.com/sivchari/rask/internal/manifests"
)

// seedsSubdir is where seed files live under a rask home directory:
// homeDir/seeds/<Key>.db.
const seedsSubdir = "seeds"

// Key identifies which seed file applies to a create request for
// (k8sVersion, coreDNSImage): "<k8sVersion>-<manifest bundle digest>".
//
// What's included, and why:
//   - k8sVersion: a different kube-apiserver/controller-manager version can
//     bootstrap a different built-in RBAC policy or set of API
//     groups/versions into the datastore.
//   - manifests.BundleDigest(coreDNSImage): the exact CoreDNS and
//     local-path-provisioner objects a seed bakes in, including the CoreDNS
//     image reference. If any of it changes (bundle content OR
//     --coredns-image), a seed built against the old content/image would
//     keep matching this key while silently serving stale objects (or the
//     wrong image) that no longer match what applying the current code
//     would produce.
//
// What's deliberately excluded:
//   - anything that is a create-time-only apiserver flag rather than stored
//     datastore content, e.g. --api-audience or --apiserver-arg: it changes
//     kube-apiserver's command line, not any object the datastore holds,
//     so it has no bearing on whether a given seed file is valid to reuse.
//   - cluster name / PKI: regenerated fresh on every create regardless of
//     seed use (see internal/bootstrap.generatePKI), and cluster identity
//     (node name, service/pod CIDRs) is fixed across every rask cluster —
//     see internal/cluster's package doc for why that's true independent of
//     prebaking.
func Key(k8sVersion, coreDNSImage string) string {
	return k8sVersion + "-" + manifests.BundleDigest(coreDNSImage)
}

// Path returns the file a seed for (k8sVersion, coreDNSImage) is (or would
// be) stored at, rooted at homeDir (as returned by cluster.DefaultHomeDir).
func Path(homeDir, k8sVersion, coreDNSImage string) string {
	return filepath.Join(homeDir, seedsSubdir, Key(k8sVersion, coreDNSImage)+".db")
}
