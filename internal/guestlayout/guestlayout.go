// Package guestlayout defines the fixed set of absolute paths inside a rask
// vz guest that both sides of the boot process must agree on: the host
// (internal/substrate/vz), which builds the template initramfs a component
// binary at a time, and the guest (cmd/rask-init), which execs them from
// there. Centralizing these paths as constants (rather than string literals
// duplicated on both sides) turns a path-typo into a compile error instead
// of a boot-time "exec: no such file or directory".
//
// Every component binary is baked into the template initramfs at create
// time (internal/components.Cache.Ensure, resolved on the host); rask-init
// never downloads anything at boot.
package guestlayout

import "github.com/sivchari/rask/internal/components"

// Fixed guest-side directories. BinDir holds every Kubernetes/kine/runc
// binary at its original basename; CNIBinDir holds the CNI plugins;
// ModulesDir holds the guest kernel's modules.dep and every .ko.gz module
// rask-init may load. The iptables and e2fsprogs bundles
// (internal/components.EnsureIPTablesBundle/EnsureE2fsprogsBundle) are
// merged directly into the guest root (lib/, usr/lib/, usr/sbin/, sbin/),
// not under a rask-specific prefix, because the binaries they contain have
// their dynamic linker and plugin search paths compiled in at those exact
// absolute locations.
const (
	BinDir     = "/opt/rask/bin"
	CNIBinDir  = "/opt/rask/cni-bin"
	ModulesDir = "/opt/rask/modules"

	CACertPath = "/etc/ssl/certs/ca-certificates.crt"

	// DataDiskDevice is the guest's virtio-blk data disk: the per-cluster
	// disk internal/substrate/vz creates host-side with os.Truncate,
	// formatted with mkfs.ext4 on first boot and mounted at
	// DataMountPoint. It is the guest's ONLY virtio-blk device (/dev/vda,
	// not /dev/vdb) — unlike a typical VM, root here is the
	// initramfs/tmpfs (see cmd/rask-init's switchRoot), not a disk image,
	// so there is no separate root block device ahead of it. Confirmed
	// live via the kernel's own boot log: "virtio_blk virtio2: [vda] ...".
	DataDiskDevice = "/dev/vda"
	DataMountPoint = "/var"

	RosettaMount = "/mnt/rosetta"

	// GuestAgentDataDir is where bootstrap.Config.DataDir points once
	// DataDiskDevice is mounted at DataMountPoint: everything
	// bootstrap.Boot writes (PKI, datastore, containerd root/state,
	// kubelet root, CNI config) lives on the persistent disk, not tmpfs.
	GuestAgentDataDir = DataMountPoint + "/lib/rask"
)

// Paths returns the components.Paths pointing at every binary's fixed
// location inside BinDir/CNIBinDir, matching the basenames
// internal/components.Cache.Ensure resolves on the host (the initramfs
// builder copies each one to BinDir/<basename> unchanged).
func Paths() *components.Paths {
	bin := func(name string) string { return BinDir + "/" + name }

	return &components.Paths{
		KubeAPIServer:         bin("kube-apiserver"),
		KubeControllerManager: bin("kube-controller-manager"),
		KubeScheduler:         bin("kube-scheduler"),
		Kubelet:               bin("kubelet"),
		KubeProxy:             bin("kube-proxy"),
		Kubectl:               bin("kubectl"),
		Kine:                  bin("kine"),
		Containerd:            bin("containerd"),
		ContainerdBinDir:      BinDir,
		Runc:                  bin("runc"),
		CNIBinDir:             CNIBinDir,
	}
}
