//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/sivchari/rask/internal/guestlayout"
)

// mount mounts source at target with fstype/flags, creating target first.
// Failures are logged to the console (our only output channel this early in
// boot) but do not abort boot on their own — callers that need a mount to
// have actually succeeded check for it explicitly (e.g. formatAndMountDataDisk).
func mount(source, target, fstype string, flags uintptr, data string) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Printf("rask-init: mkdir %s: %v\n", target, err)

		return
	}

	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		fmt.Printf("rask-init: mount %s on %s (%s): %v\n", source, target, fstype, err)
	}
}

// mountBase mounts the pseudo-filesystems every later stage depends on. It
// is called twice: once immediately at boot (from the initramfs), and once
// again right after switchRoot (see main.go's comment on why — these
// mounts are not carried across the tmpfs move automatically).
func mountBase() {
	mount("proc", "/proc", "proc", 0, "")
	mount("sysfs", "/sys", "sysfs", 0, "")
	mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	mount("cgroup2", "/sys/fs/cgroup", "cgroup2", 0, "")
	mount("binfmt_misc", "/proc/sys/fs/binfmt_misc", "binfmt_misc", 0, "")

	for _, dir := range []string{
		guestlayout.RosettaMount,
		"/run/containerd",
		"/var/lib/containerd",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Printf("rask-init: mkdir %s: %v\n", dir, err)
		}
	}
}

// newRoot is the tmpfs mount switchRoot moves the running system onto.
const newRoot = "/newroot"

// excludedFromCopy are absolute paths switchRoot does not copy into newRoot:
// pseudo-filesystems that get mounted fresh again after chroot (see
// mountBase's doc comment) and newRoot itself.
var excludedFromCopy = map[string]bool{
	"/proc": true, "/sys": true, "/dev": true, newRoot: true,
}

// switchRoot performs rask's tmpfs switch_root trick (plan-m0-spikes.md's
// guest layout decision): mount a fresh tmpfs, copy the initramfs's entire
// content into it, then chroot into it. The resulting root is a real mount
// point (a tmpfs vfsmount, not the initramfs's anonymous rootfs), which is
// what lets runc's pivot_root(2) succeed later — spikes/s3 needed
// `ctr run --no-pivot` specifically because its rootfs was the unmounted
// initramfs, which always fails pivot_root with EINVAL.
//
// Unlike util-linux's switch_root (which discards the old root and execs a
// new init binary from the new one), rask-init keeps running as the same
// process afterward: a Go binary's already-loaded text/data segments are
// unaffected by chroot, which only changes future path resolution, so no
// re-exec is needed.
func switchRoot() error {
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", newRoot, err)
	}

	if err := syscall.Mount("tmpfs", newRoot, "tmpfs", 0, ""); err != nil {
		return fmt.Errorf("mount tmpfs on %s: %w", newRoot, err)
	}

	if err := copyTree("/", newRoot); err != nil {
		return fmt.Errorf("copying initramfs into %s: %w", newRoot, err)
	}

	// The switch_root(8) sequence: move newRoot's mount to be the root
	// mount, then chroot into (now-relocated) ".", then chdir to "/".
	if err := syscall.Chdir(newRoot); err != nil {
		return fmt.Errorf("chdir %s: %w", newRoot, err)
	}

	if err := syscall.Mount(newRoot, "/", "", syscall.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move-mounting %s onto /: %w", newRoot, err)
	}

	if err := syscall.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}

	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir / after chroot: %w", err)
	}

	return nil
}

// copyTree recursively copies every file, directory and symlink under src
// into dst, skipping excludedFromCopy paths and preserving symlink targets
// and regular file permissions (device nodes are deliberately not copied:
// /dev is repopulated by a fresh devtmpfs mount after switchRoot, not by
// carrying initramfs-era device nodes into the tmpfs root).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path != "/" && excludedFromCopy[path] {
			return fs.SkipDir
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if rel == "." {
			return nil
		}

		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}

			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			// Device/socket/pipe nodes: none expected under the
			// initramfs paths this walks (devtmpfs lives at /dev,
			// excluded above), skip defensively rather than fail
			// the whole switch_root over an unexpected node.
			return nil
		}
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}

	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}

	return nil
}

// formatAndMountDataDisk formats the per-cluster virtio-blk data disk with
// ext4 (using the bundled mkfs.ext4, internal/components.EnsureE2fsprogsBundle)
// and mounts it at guestlayout.DataMountPoint. v1 always reformats: rask
// creates a fresh sparse disk file per cluster and this guest never reboots
// within one cluster's lifetime (create boots once; delete tears down), so
// there is no existing filesystem to preserve — unlike a future snapshot/
// restore path, which would need to skip this and mount the existing
// filesystem instead.
func formatAndMountDataDisk() error {
	mkfs := exec.Command("/sbin/mkfs.ext4", "-F", "-q", guestlayout.DataDiskDevice)
	mkfs.Stdout = os.Stdout
	mkfs.Stderr = os.Stdout

	if err := mkfs.Run(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %w", guestlayout.DataDiskDevice, err)
	}

	if err := os.MkdirAll(guestlayout.DataMountPoint, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", guestlayout.DataMountPoint, err)
	}

	if err := syscall.Mount(guestlayout.DataDiskDevice, guestlayout.DataMountPoint, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount %s on %s: %w", guestlayout.DataDiskDevice, guestlayout.DataMountPoint, err)
	}

	return nil
}

// setGuestPath sets PATH in rask-init's own process environment so every
// child process bootstrap.Supervisor launches with a nil ProcessSpec.Env
// (which os/exec resolves against the calling process's environment)
// inherits it — required for kube-proxy's iptables mode, which execs
// "iptables-save"/"iptables-restore" by bare name via PATH, not a full
// path.
func setGuestPath() {
	path := guestlayout.BinDir + ":" + guestlayout.CNIBinDir + ":/usr/sbin:/sbin:/usr/bin:/bin"
	_ = os.Setenv("PATH", path)
}
