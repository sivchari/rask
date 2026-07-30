// Package manifests applies the cluster add-ons every rask cluster needs
// beyond the control plane and node itself: CoreDNS (coredns.go, built as
// typed client-go objects) and local-path-provisioner plus a default
// StorageClass (embedded from local-path-storage.yaml, applied via
// apply.go's generic multi-document YAML applier). Callers apply these
// once the API server is Ready.
package manifests
