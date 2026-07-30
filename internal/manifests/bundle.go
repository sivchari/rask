package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// BundleDigest returns a hex-encoded sha256 digest over the exact content of
// every object ApplyCoreDNS and ApplyLocalPathProvisioner create: the
// "default manifest bundle" every rask cluster gets during Start.
//
// internal/prebake's seed key includes this digest so a seed built against
// an older bundle is never reused after the bundle changes: the seed's
// datastore would otherwise still contain the old CoreDNS/local-path-provisioner
// objects, silently drifting from what applying the current code would
// produce.
func BundleDigest() string {
	h := sha256.New()

	for _, obj := range []any{
		coreDNSServiceAccount(),
		coreDNSClusterRole(),
		coreDNSClusterRoleBinding(),
		coreDNSConfigMap(),
		coreDNSDeployment(),
		coreDNSService(),
	} {
		// These are all plain, statically-typed k8s API objects (no
		// channels/funcs/cyclic refs); json.Marshal on them cannot fail.
		b, _ := json.Marshal(obj)
		h.Write(b)
	}

	h.Write(localPathStorageYAML)

	return hex.EncodeToString(h.Sum(nil))
}
