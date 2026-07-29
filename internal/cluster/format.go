package cluster

import "strings"

// contextNamePlaceholder is substituted with the cluster name in a
// --context-format value.
const contextNamePlaceholder = "{name}"

// FormatContext renders a kubeconfig context name from format (e.g. the
// rask default "rask-{name}", or the kind-compatible "kind-{name}" that
// tools like Tilt auto-trust) and a cluster name.
func FormatContext(format, name string) string {
	return strings.ReplaceAll(format, contextNamePlaceholder, name)
}
