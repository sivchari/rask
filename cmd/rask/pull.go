package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sivchari/rask/internal/components/bundlepayload"
	"github.com/sivchari/rask/internal/substrate"
)

// pullRuntimeName is the placeholder cluster name newPullCommand passes to
// Runtime.Create. Both substrate.Runtime implementations (hostproc, vz)
// ignore it entirely: Create only ever warms the shared, host-wide
// component cache (and, deliberately, never touches any
// cluster-name-scoped state — see each implementation's own doc comment on
// why a failed Create must leave no trace), so any name is safe here. A
// descriptive placeholder rather than "" purely for readability in logs.
const pullRuntimeName = "pull"

// newPullCommand builds "rask pull": materializes every component a
// cluster needs into rt's local cache ahead of time, so a later "rask
// create cluster" needs no network at all. This is exactly what
// Runtime.Create already does (see hostproc.Runtime.Create and
// vz.Runtime.Create's doc comments) — resolve and cache every component
// binary/bundle, with no cluster-specific side effects — so pull is a thin
// wrapper around it, not a second implementation of cache warming.
//
// Refuses to run against a non-bundled build instead of silently falling
// through to a live download: rask no longer ships a slim, download-on-
// first-use binary (see .goreleaser.yml), so a build with no embedded
// payload has nothing for pull to materialize without network access, the
// entire point of the command. This is the EBS image-cache bake step haro
// workspaces run: `rask pull` against an empty ~/.rask/cache, from a
// bundled binary, with no network available at all.
func newPullCommand(rt substrate.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Materialize every cluster component into the local cache, without network",
		Long: `Materialize every component a "rask create cluster" needs (Kubernetes
binaries, kine, containerd, runc, the CNI plugins, and, on macOS, the vz
guest kernel and userland bundles) into the local cache, from this binary's
embedded payload — no network access required.

Only works on a bundled binary (every published release; see
.goreleaser.yml). A binary built from source without "make bundle" has no
embedded payload to extract from, and fails with an explanatory error
instead of silently downloading one: run "make bundle-payload && make
bundle", or just "rask create cluster", which downloads on demand.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !bundlepayload.Available() {
				return fmt.Errorf(
					"rask pull: this binary has no embedded component payload; " +
						"download a released rask-bundled binary, or from a rask source checkout run `make bundle-payload && make bundle`",
				)
			}

			return rt.Create(cmd.Context(), pullRuntimeName, substrate.StartOptions{})
		},
	}
}
