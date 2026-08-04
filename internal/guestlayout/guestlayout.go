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

	// PrebootDir is StartOptions.PrebootFiles' documented, external,
	// in-guest destination (see substrate.PrebootSubdir's doc comment for
	// the absolute-path formula a caller like fjord computes) — but
	// rask-init never writes anything there directly during boot; see
	// PrebootStagingDir for where the content actually lands first, and
	// why.
	PrebootDir = GuestAgentDataDir + "/preboot"

	// PrebootStagingDir is where internal/substrate/vz's preboot overlay
	// cpio (see that package's preboot.go) actually places
	// StartOptions.PrebootFiles content, concatenated onto the template
	// initramfs so it exists from the moment the kernel unpacks the
	// initramfs, before rask-init ever runs.
	//
	// It is NOT PrebootDir itself: PrebootDir sits under GuestAgentDataDir
	// (i.e. under DataMountPoint, "/var"), which cmd/rask-init's
	// formatAndMountDataDisk mounts the per-cluster ext4 data disk over —
	// well after the initramfs (and its preboot overlay) is already
	// unpacked into the tmpfs root. Mounting over /var would silently
	// shadow whatever the overlay placed at PrebootDir, standard Unix
	// mount semantics, making every preboot file invisible by the time
	// bootstrap.Boot (and anything it launches, e.g. a kube-apiserver
	// --apiserver-arg referencing one) actually runs — found while wiring
	// StartOptions.ExtraAPIServerArgs through to vz, since that is the
	// first caller that would ever have actually opened a file under
	// PrebootDir. rask-init copies this staging directory's content into
	// PrebootDir once the data disk is mounted (see cmd/rask-init's
	// copyPrebootFiles), so PrebootDir's own external contract still
	// resolves correctly for a caller like fjord.
	PrebootStagingDir = "/opt/rask/preboot"

	// ClusterConfigPath is where internal/substrate/vz's cluster-config
	// overlay cpio places a small JSON-encoded internal/guestconfig.Config,
	// for rask-init to read at boot: StartOptions fields
	// (ExtraAPIServerArgs, CoreDNSImage) that don't fit as a single kernel
	// command-line token the way ClusterName/BootTimeUnixNano do (see
	// internal/guestinit/bootparam.go).
	ClusterConfigPath = "/opt/rask/cluster-config.json"

	// ImagesDir is where internal/substrate/vz's images overlay cpio
	// places each host-prefetched cluster image's docker-save style tar
	// archive (see internal/imagebundle), for rask-init to import into the
	// guest's own containerd once it comes up — mirroring
	// internal/substrate/hostproc.Runtime's importCachedImages, adapted
	// for a guest with no host-reachable containerd socket of its own to
	// dial from outside (see vz's LoadImages doc comment for that gap).
	ImagesDir = "/opt/rask/images"
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
