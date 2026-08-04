// Package guestconfig defines the small per-cluster JSON configuration file
// internal/substrate/vz's host side writes into the guest via its
// cluster-config overlay cpio (see guestlayout.ClusterConfigPath), and
// cmd/rask-init reads at boot. It exists for StartOptions fields that,
// unlike the cluster name and boot time (carried via the kernel command
// line — see internal/guestinit/bootparam.go), don't fit cleanly as a
// single command-line token: a repeatable apiserver flag list and a CoreDNS
// image reference.
//
// Has no OS dependencies of its own (like internal/guestagent), so it
// builds and tests on any host: internal/substrate/vz (darwin-only) writes
// it, cmd/rask-init (linux-only) reads it.
package guestconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Config is every StartOptions field internal/substrate/vz threads through
// to the guest via a cluster-config overlay cpio, rather than through
// bootstrap.Boot's other inputs (kernel command line, preboot files).
type Config struct {
	// ExtraAPIServerArgs mirrors substrate.StartOptions.ExtraAPIServerArgs,
	// passed straight through to bootstrap.Config.ExtraAPIServerArgs.
	ExtraAPIServerArgs []string `json:"extraAPIServerArgs,omitempty"`

	// CoreDNSImage mirrors substrate.StartOptions.CoreDNSImage. Empty
	// means "use rask's own default" (manifests.CoreDNSImage) — the same
	// zero-value convention substrate.StartOptions itself uses.
	CoreDNSImage string `json:"coreDNSImage,omitempty"`
}

// IsZero reports whether cfg carries no overrides at all, i.e. whether
// writing it into an overlay would be pointless.
func (cfg Config) IsZero() bool {
	return len(cfg.ExtraAPIServerArgs) == 0 && cfg.CoreDNSImage == ""
}

// Marshal encodes cfg as the JSON document written to
// guestlayout.ClusterConfigPath.
func Marshal(cfg Config) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("guestconfig: encoding config: %w", err)
	}

	return data, nil
}

// Load reads and decodes the Config at path. A missing file is not an
// error: it returns a zero Config, matching every field's own
// "empty/unset means use rask's default" convention — the overwhelmingly
// common case of a "rask create" with no --apiserver-arg or --coredns-image
// never has a cluster-config overlay staged at all (see
// internal/substrate/vz's clusterconfig.go).
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}

	if err != nil {
		return Config{}, fmt.Errorf("guestconfig: reading %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("guestconfig: decoding %s: %w", path, err)
	}

	return cfg, nil
}
