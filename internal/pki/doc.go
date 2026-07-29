// Package pki generates the self-signed certificate authority and leaf
// certificates a rask cluster needs to run its control plane and to
// authenticate kubectl clients. It does not depend on cert-manager: rask
// creates the CA itself, issues server certificates for the API server and
// client certificates for kubeconfig users, and can render a ready-to-use
// kubeconfig file from the result.
package pki
