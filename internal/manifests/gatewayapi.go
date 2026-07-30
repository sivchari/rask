package manifests

import (
	"context"
	_ "embed"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
)

// gatewayAPICRDsYAML is the CRD-only "standard" channel install manifest
// vendored from
// https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml
// (v1.5.1 matches the sigs.k8s.io/gateway-api version the haro operator
// dogfooding target imports). CRDs only, no controller: haro's exit
// criteria only needs the Gateway API types to exist as installable CRDs,
// not a running Gateway API implementation.
//
//go:embed gatewayapi-crds.yaml
var gatewayAPICRDsYAML []byte

// ApplyGatewayAPICRDs installs the Gateway API "standard" channel CRDs
// (GatewayClass/Gateway/HTTPRoute/GRPCRoute/ReferenceGrant/...) using dyn
// and mapper. Opt-in via "rask addon install gateway-api", not applied by
// every cluster's Start.
func ApplyGatewayAPICRDs(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper) error {
	return ApplyYAML(ctx, dyn, mapper, gatewayAPICRDsYAML)
}
