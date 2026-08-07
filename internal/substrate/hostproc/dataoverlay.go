//go:build linux

package hostproc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ensureDataDirNotOverlayfs guarantees dataDir does not itself live on an
// overlayfs mount before bootstrap.Boot lets containerd start creating
// content under it, using the real statfs(2) syscall as production's
// fsType.
//
// Found live: inside a `docker run --privileged` container whose own
// rootfs is backed by Docker's overlay2 storage driver, dataDir (under
// ~/.rask/clusters, inherited from that same rootfs) is itself an
// overlayfs mount. containerd's overlayfs snapshotter then tries to layer
// another overlayfs on top of it for every image layer, which the kernel
// rejects with "invalid argument" — a known limitation of nesting
// overlayfs on overlayfs — an error that gives no hint that the data
// directory's filesystem (rather than, say, a containerd config mistake)
// is the cause.
func ensureDataDirNotOverlayfs(dataDir string) error {
	return ensureDataDirNotOverlayfsType(dataDir, statfsType)
}

// ensureDataDirNotOverlayfsType is ensureDataDirNotOverlayfs with the
// filesystem-type lookup injected so the logic is unit-testable without a
// real overlayfs mount, which requires root to create.
func ensureDataDirNotOverlayfsType(dataDir string, fsType func(path string) (int64, error)) error {
	path, err := nearestExistingAncestor(dataDir)
	if err != nil {
		return fmt.Errorf("hostproc: checking filesystem under %s: %w", dataDir, err)
	}

	magic, err := fsType(path)
	if err != nil {
		return fmt.Errorf("hostproc: checking filesystem of %s: %w", path, err)
	}

	if magic != unix.OVERLAYFS_SUPER_MAGIC {
		return nil
	}

	return fmt.Errorf("hostproc: %s is on an overlayfs mount (statfs f_type=%#x): containerd's overlayfs snapshotter cannot layer image content onto a data directory that is itself overlayfs — nested overlayfs is rejected by the kernel with \"invalid argument\"; move rask's data directory (--home, or $HOME/.rask) onto a non-overlayfs mount, e.g. a tmpfs or a dedicated volume, or run rask outside of a container whose own rootfs is overlayfs-backed", path, magic)
}

// nearestExistingAncestor walks up from path until it finds a directory
// that already exists, returning it. dataDir (and likely its cluster.Dir
// parent) doesn't exist yet when Start runs — a MkdirAll further down
// bootstrap.Boot's write path is what first creates it — but a directory
// later created under an existing directory inherits that directory's
// mount (barring another mount later grafted in between), so statfs-ing
// the nearest existing ancestor answers the same question a statfs on
// dataDir itself would once it exists.
func nearestExistingAncestor(path string) (string, error) {
	dir := filepath.Clean(path)

	for {
		if _, err := os.Stat(dir); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding anything that
			// exists; "/" always exists on a running system, so this is
			// unreachable in practice.
			return "", fmt.Errorf("no existing ancestor found above %s", path)
		}

		dir = parent
	}
}

// statfsType is ensureDataDirNotOverlayfs's production fsType: the real
// statfs(2) f_type of path.
func statfsType(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}

	return int64(st.Type), nil
}
