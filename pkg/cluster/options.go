package cluster

import "io"

// WaitNode and WaitCoreDNS are the allowed Options.Wait values, mirroring
// "rask create cluster --wait".
const (
	WaitNode    = "node"
	WaitCoreDNS = "coredns"
)

// Options configures Provider.Create. Every field mirrors a "rask create
// cluster" flag (see cmd/rask/create.go); a consumer driving rask as a
// library has the identical set of knobs a consumer shelling out to the CLI
// would.
type Options struct {
	// Wait is what Create blocks on before returning: WaitNode (the
	// default, used if empty) or WaitCoreDNS.
	Wait string

	// ExtraAPIAudiences are additional kube-apiserver --api-audiences
	// values beyond the cluster's own service-account issuer, e.g. for a
	// TokenReview client that requests a custom audience.
	ExtraAPIAudiences []string

	// ExtraAPIServerArgs are additional "key=value" kube-apiserver flags
	// (kubeadm-style, no leading "--"), appended to the flags rask itself
	// sets. A key naming one of rask's own managed flags is rejected with
	// an error rather than silently overridden.
	ExtraAPIServerArgs []string

	// PrebootFiles are copied into the cluster's data directory before
	// any cluster process starts, so their destination path can be
	// referenced by an ExtraAPIServerArgs value (e.g. an authentication
	// webhook kubeconfig kube-apiserver should read at startup). See
	// PrebootFile's doc comment for the destination path convention.
	PrebootFiles []PrebootFile

	// ComponentDir, if set, overrides rask's default dl.k8s.io download
	// cache for kube-apiserver/kube-controller-manager/kube-scheduler/
	// kubelet/kubectl with pre-extracted binaries from this local
	// directory. Every other component (kube-proxy, kine, containerd,
	// runc, the CNI plugins) still comes from the default download cache.
	ComponentDir string

	// CoreDNSImage, if set, overrides rask's default CoreDNS image for
	// this cluster's CoreDNS Deployment.
	CoreDNSImage string
}

// PrebootFile is a single file, copied from the filesystem of the process
// calling Provider.Create into the new cluster's data directory before any
// cluster process starts.
//
// Dest is a slash-separated path relative to the cluster's data directory's
// "preboot" subdirectory; it may not contain ".." segments or be absolute.
// Its absolute path (for referencing from an ExtraAPIServerArgs value) is
// filepath.Join(dataDir, "preboot", Dest), where dataDir is
// <homeDir>/clusters/<name>/data for the default (hostproc) Provider.
type PrebootFile struct {
	// Src is a path on the filesystem of the process calling
	// Provider.Create, readable at Create time.
	Src string

	// Dest is Src's destination, relative to the cluster's preboot
	// directory (see the PrebootFile doc comment above).
	Dest string
}

// Result is what Provider.Create returns once the requested Options.Wait
// condition is satisfied.
type Result struct {
	// KubeconfigPath is the same value Provider.KubeConfigPath(name)
	// returns, repeated here since it's what most callers actually need
	// right after a successful Create.
	KubeconfigPath string

	// Timeline is a best-effort, human-readable phase-by-phase boot
	// latency breakdown, if the underlying Provider produced one. Empty
	// if unavailable; never an error condition on its own.
	Timeline string
}

// ExportOptions configures Provider.KubeConfig.
type ExportOptions struct {
	// ContextFormat renders the kubeconfig's cluster/user/context name
	// (e.g. "kind-{name}", auto-trusted by tools like Tilt); "{name}" is
	// replaced with the cluster name. Empty (the default) leaves the
	// context named exactly the cluster name, unlike "rask export
	// kubeconfig"'s own CLI default ("rask-{name}") — a library caller
	// gets back exactly what rask wrote unless it explicitly opts into a
	// rename.
	ContextFormat string
}

// ImageSource is a single container image, as an OCI/docker archive tar
// stream (e.g. the output of "docker save"), to import into a cluster's
// image store via Provider.LoadImages. Mirrors
// internal/substrate.ImageSource without exposing that internal type
// directly in this package's public API.
type ImageSource struct {
	// Reference identifies the image for progress/error reporting only;
	// LoadImages does not use it to select what gets imported — the
	// archive in Stream already carries its own name(s)/tag(s).
	Reference string

	// Stream is the archive's content, read to exhaustion exactly once
	// and not closed; the caller retains ownership of its lifetime.
	Stream io.Reader
}
