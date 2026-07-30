package guestinit

// WantedModules is the fixed set of kernel modules rask-init loads at boot
// (by bare name — see ResolveLoadOrder for the "-"/"_" matching rule),
// resolved against the guest kernel's modules.dep for their full
// transitive dependency closure. Both sides of the host/guest boundary
// share this single list: the host (internal/substrate/vz's initramfs
// builder) uses it to decide exactly which .ko.gz files to copy into the
// template initramfs (avoiding bundling the guest kernel's entire,
// much-larger module tree), and the guest (cmd/rask-init) uses the same
// list against the same modules.dep to compute the same load order.
//
// Categories, each verified present in the pinned Alpine linux-virt kernel
// (components.GuestKernelRelease) by inspecting its real modules.dep:
//
//   - boot essentials (spikes/s3/RESULTS.md's production recipe): entropy,
//     packet/failover plumbing virtio_net depends on, FUSE/virtiofs (for
//     the Rosetta directory share), binfmt_misc, overlay (containerd's
//     snapshotter).
//   - data disk: virtio_blk (the per-cluster data disk device) and ext4
//     (its filesystem).
//   - bridge CNI: bridge, veth, br_netfilter (bridge CNI plugin +
//     containerd's CNI wiring).
//   - netfilter/iptables: everything kube-proxy's iptables mode needs
//     (ip(6)_tables and the iptable_*/xt_*/nf_* modules its rendered
//     iptables-restore rule set exercises). Bundling this whole family
//     rather than hand-picking the minimal subset is a deliberate
//     simplification (each module is a few KB; the safety margin against a
//     kube-proxy CrashLoop from one missing xt_* match extension is worth
//     more than the bytes saved) — see plan-m0-spikes.md's networking
//     section ("if an entire iptables module family is missing... prefer
//     fixing modules").
//   - crc32c_generic: registers the "crc32c" crypto API shash algorithm
//     that lib/libcrc32c.ko's crc32c() and nf_conntrack's init both look
//     up via crypto_alloc_shash("crc32c", ...). modules.dep doesn't record
//     it as a dependency of anything (the crypto API resolves it by
//     algorithm name at runtime, not by symbol linkage depmod can see), so
//     without loading it explicitly and before those modules, their own
//     init() functions fail — found live: nf_conntrack (and everything
//     that links against its exported symbols: nf_nat, iptable_nat,
//     xt_nat, xt_conntrack, xt_MASQUERADE, xt_CT) failed to load with
//     "Unknown symbol nf_ct_*" until this was added first in this list.
var WantedModules = []string{
	"crc32c_generic",

	"rng-core", "virtio-rng", "af_packet", "failover", "net_failover",
	"virtio_net", "fuse", "virtiofs", "binfmt_misc", "overlay",

	"virtio_blk", "ext4",

	"bridge", "veth", "br_netfilter",

	"ip_tables", "iptable_filter", "iptable_nat", "iptable_mangle",
	"ip6_tables", "ip6table_filter", "ip6table_nat", "ip6table_mangle",
	"nf_nat", "nf_conntrack", "nf_defrag_ipv4", "nf_defrag_ipv6",
	"x_tables", "xt_addrtype", "xt_comment", "xt_mark", "xt_multiport",
	"xt_nat", "xt_recent", "xt_statistic", "xt_tcpudp", "xt_conntrack",
	"xt_MASQUERADE", "xt_CT",
}
