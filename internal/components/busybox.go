package components

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// busyboxPackage is the pinned Alpine aarch64 busybox package. Dynamically
// linked against musl (already bundled for iptables/e2fsprogs — see
// iptables.go/e2fsprogs.go — so no new shared library dependency), no
// other deps.
var busyboxPackage = iptablesPackage{name: "busybox", version: "1.37.0-r14", sha256: "35e35183161c613ef50c61fcd96a0bee1a4d4ac60127d4c4941e83c0e0cdf028"}

// BusyboxBundleVersion identifies this pinned package for cache directory
// naming.
const BusyboxBundleVersion = "1.37.0-r14"

// busyboxMountApplets are the busybox applet names rask-init needs
// available as symlinks to bin/busybox (busybox dispatches by argv[0],
// the same multi-call-binary mechanism as iptables.go's xtables-nft-multi).
//
// Alpine's busybox package (unlike its `.deb`/`.rpm` counterparts on other
// distros) does not ship these symlinks in the apk itself — they're
// normally created by a post-install trigger script that runs busybox's
// own "--install" self-registration, which this package doesn't execute
// (files are extracted directly, not installed via apk). They're created
// explicitly by EnsureBusyboxBundle instead.
//
// Only "mount"/"umount" are needed right now: kubelet's projected-volume
// plugin (ServiceAccount token / ConfigMap / DownwardAPI mounts) execs the
// external "mount" command rather than calling the syscall directly for
// some volume types — found live ("MountVolume.SetUp failed ... mount:
// executable file not found in $PATH") blocking CoreDNS's pod from ever
// starting. This is a narrow, deliberate exception to the rest of this
// guest's "no shell, no busybox" design (see cmd/rask-init's package doc):
// only the specific applets a real dependency needs, not a general-purpose
// shell environment.
var busyboxMountApplets = []string{"mount", "umount"}

// BusyboxBundle is the resolved busybox binary plus the mount/umount
// symlinks pointing at it.
type BusyboxBundle struct {
	Dir string
}

// EnsureBusyboxBundle downloads (if not already cached), verifies and
// extracts the busybox binary, creating the busyboxMountApplets symlinks
// at usr/sbin/<applet> -> ../../bin/busybox.
func (c *Cache) EnsureBusyboxBundle(ctx context.Context) (*BusyboxBundle, error) {
	dir := filepath.Join(c.dir, "busybox-"+BusyboxBundleVersion)
	marker := filepath.Join(dir, "bin", "busybox")

	if _, err := os.Stat(marker); err == nil {
		return &BusyboxBundle{Dir: dir}, nil
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	data, err := c.fetch(ctx, busyboxPackage.url())
	if err != nil {
		return nil, fmt.Errorf("components: downloading busybox: %w", err)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != busyboxPackage.sha256 {
		return nil, fmt.Errorf("components: busybox sha256 mismatch: got %s, want %s", got, busyboxPackage.sha256)
	}

	if err := extractTarGzPreserveSymlinks(data, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)

		return nil, fmt.Errorf("components: extracting busybox: %w", err)
	}

	for _, applet := range busyboxMountApplets {
		target := filepath.Join(tmpDir, "usr", "sbin", applet)
		if err := extractSymlink(tmpDir, "../../bin/busybox", target); err != nil {
			_ = os.RemoveAll(tmpDir)

			return nil, fmt.Errorf("components: linking busybox applet %s: %w", applet, err)
		}
	}

	if err := os.Rename(tmpDir, dir); err != nil {
		return nil, fmt.Errorf("components: finalizing %s: %w", dir, err)
	}

	return &BusyboxBundle{Dir: dir}, nil
}
