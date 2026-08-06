//go:build darwin

package vz

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/guestconfig"
	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate"
)

// clusterConfigFileName is where Runtime.Start stages the guest-bound
// internal/guestconfig.Config for RunVMHost — a separate process — to read
// back and embed into the cluster-config overlay cpio (see
// buildClusterConfigCpio). Mirrors PrebootFiles' own staging pattern
// (substrate.StagePrebootFiles / buildPrebootCpio): both exist because
// Runtime.Start and RunVMHost are different OS processes with nothing but
// dataDir in common.
const clusterConfigFileName = "cluster-config.json"

// guestConfigFrom builds the internal/guestconfig.Config carrying every
// StartOptions field that has no other transport to the guest (the kernel
// command line only carries ClusterName/BootTimeUnixNano — see
// internal/guestinit/bootparam.go — and PrebootFiles have their own cpio
// overlay).
func guestConfigFrom(opts substrate.StartOptions) guestconfig.Config {
	return guestconfig.Config{
		ExtraAPIServerArgs: opts.ExtraAPIServerArgs,
		CoreDNSImage:       opts.CoreDNSImage,
	}
}

// stageClusterConfig writes opts' guestConfigFrom to dataDir, for
// buildClusterConfigCpio (run later, from RunVMHost) to pick up. A no-op —
// leaving nothing on disk — if opts carries nothing worth transporting,
// so the overwhelmingly common "rask create" with neither --apiserver-arg
// nor --coredns-image never even creates the file, matching
// StagePrebootFiles' own "no-op if empty" contract.
func stageClusterConfig(dataDir string, opts substrate.StartOptions) error {
	cfg := guestConfigFrom(opts)
	if cfg.IsZero() {
		return nil
	}

	data, err := guestconfig.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("vz: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", dataDir, err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, clusterConfigFileName), data, 0o600); err != nil {
		return fmt.Errorf("vz: writing staged cluster config: %w", err)
	}

	return nil
}

// loadStagedClusterConfig reads back what stageClusterConfig staged,
// returning a zero Config (not an error) if Runtime.Start never staged one
// — the common case. Used by RunVMHost both to build the cluster-config
// overlay cpio and to decide which CoreDNS image to prefetch (see
// imageprefetch.go).
func loadStagedClusterConfig(dataDir string) (guestconfig.Config, error) {
	cfg, err := guestconfig.Load(filepath.Join(dataDir, clusterConfigFileName))
	if err != nil {
		return guestconfig.Config{}, fmt.Errorf("vz: %w", err)
	}

	return cfg, nil
}

// buildClusterConfigCpio returns a cpio archive containing cfg JSON-encoded
// at guestlayout.ClusterConfigPath, or a zero-length (but non-nil) slice if
// cfg carries nothing — mirroring buildPrebootCpio's same-shaped contract,
// so RunVMHost can skip initramfs concatenation entirely by checking len()
// in the common case.
func buildClusterConfigCpio(cfg guestconfig.Config) ([]byte, error) {
	if cfg.IsZero() {
		return []byte{}, nil
	}

	data, err := guestconfig.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("vz: %w", err)
	}

	w := newCpioWriter()
	if err := w.WriteFile(strings.TrimPrefix(guestlayout.ClusterConfigPath, "/"), 0o644, data); err != nil {
		return nil, fmt.Errorf("vz: writing cluster config into overlay: %w", err)
	}

	return w.Finish()
}
