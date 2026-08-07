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
//   - boot essentials (the M0 s3 spike's production recipe): entropy,
//     packet/failover plumbing virtio_net depends on, FUSE/virtiofs (for
//     the Rosetta directory share), binfmt_misc, overlay (containerd's
//     snapshotter).
//   - data disk: virtio_blk (the per-cluster data disk device) and ext4
//     (its filesystem).
//   - bridge CNI: bridge, veth, br_netfilter (bridge CNI plugin +
//     containerd's CNI wiring).
//   - dummy: registers the "dummy" net driver (drivers/net/dummy.ko), which
//     backs vishvananda/netlink's netlink.Dummy link type used by upstream
//     agents to create a stand-alone virtual interface with no peer —
//     found live via the eks-pod-identity-agent, which creates one
//     (pod-id-link0) to hold the link-local IPv4 address it uses to proxy
//     the container credentials endpoint. Without this module, RTNETLINK
//     rejects the RTM_NEWLINK request outright ("ip link add pod-id-link0
//     type dummy" => "RTNETLINK answers: Not supported"), which the agent
//     logs as "Unable to create interface pod-id-link0: operation not
//     supported" and then fails to start.
//   - netfilter/iptables (legacy API): everything kube-proxy's iptables
//     mode needs (ip(6)_tables and the iptable_*/xt_*/nf_* modules its
//     rendered iptables-restore rule set exercises).
//   - netfilter/nftables (nf_tables API): the bundled iptables binary
//     (internal/components/iptables.go) is Alpine's xtables-nft-multi —
//     the nft-BACKED implementation of the iptables command line, not the
//     legacy ip_tables/x_tables-only one — so it needs the nf_tables
//     kernel subsystem itself, not just the legacy modules above. Missing
//     this entirely (only the legacy family was bundled at first) is what
//     caused kube-proxy's own iptables detection
//     (k8s.io/kubernetes/pkg/util/iptables's Present(), which checks for
//     the nat table's POSTROUTING chain) to fail with "iptables is not
//     available on this host" — found live.
//     Bundling both whole families rather than hand-picking the minimal
//     subset is a deliberate simplification (each module is a few KB; the
//     safety margin against a kube-proxy CrashLoop from one missing
//     extension is worth more than the bytes saved) — per the M0 planning
//     guidance ("if an entire iptables module family is missing... prefer
//     fixing modules").
//   - REJECT target support (nf_reject_ipv4/ipv6, ipt_REJECT/ip6t_REJECT,
//     nft_reject and its ipv4/ipv6/inet variants): kube-proxy's iptables
//     mode always programs a REJECT rule in KUBE-SERVICES for services with
//     no endpoints, in both the IPv4 and IPv6 chains regardless of whether
//     dual-stack is actually in use — without these modules,
//     iptables-restore fails outright on every sync attempt with
//     "Extension REJECT revision 0 not supported, missing kernel module?"
//     and "RULE_APPEND failed (No such file or directory): rule in chain
//     KUBE-SERVICES", so *no* Service (not just the ones that would
//     legitimately hit REJECT) ever gets routed — found live: this is what
//     kept CoreDNS's "kubernetes" plugin from ever reaching the apiserver
//     through its ClusterIP, well after the earlier "Couldn't load match
//     `mark'" case-collision bug (see internal/components/iptables.go's
//     CaseCollisionManifest) was already fixed and confirmed gone from
//     kube-proxy's log. ipt_REJECT/ip6t_REJECT are the legacy xtables
//     kernel modules nft_compat's translation of "-j REJECT" depends on
//     (this iptables binary is nft-backed, see the nftables paragraph
//     below); the nft_reject family is bundled alongside them since both
//     paths are cheap and untangling which one nft_compat actually
//     exercises isn't worth the fragility.
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
//   - xfrm_user: registers the NETLINK_XFRM netlink family
//     (net/xfrm/xfrm_user.c's netlink_kernel_create call). Without it, any
//     process that opens a NETLINK_XFRM socket — including
//     vishvananda/netlink's netlink.NewHandle() called with no arguments,
//     which unconditionally opens all of SupportedNlFamilies (ROUTE, XFRM,
//     NETFILTER) and fails entirely if even one of them errors — gets
//     EPROTONOSUPPORT, even though CONFIG_XFRM itself is built in
//     (/proc/net/xfrm_stat exists). This breaks any Go networking tool that
//     calls NewHandle() this way (found live via the upstream
//     eks-pod-identity-agent, which never touches IPsec/XFRM but still
//     fails to start over this) — Calico, Cilium, flannel and multus all
//     have the same NewHandle() call site, so this is not
//     workload-specific. xfrm_algo is not listed separately: it's
//     xfrm_user's only modules.dep dependency and ResolveLoadOrder already
//     pulls it in transitively.
var WantedModules = []string{
	"crc32c_generic",

	"rng-core", "virtio-rng", "af_packet", "failover", "net_failover",
	"virtio_net", "fuse", "virtiofs", "binfmt_misc", "overlay",

	"virtio_blk", "ext4",

	"bridge", "veth", "br_netfilter",

	"dummy",

	"xfrm_user",

	"ip_tables", "iptable_filter", "iptable_nat", "iptable_mangle",
	"ip6_tables", "ip6table_filter", "ip6table_nat", "ip6table_mangle",
	"nf_nat", "nf_conntrack", "nf_defrag_ipv4", "nf_defrag_ipv6",
	"x_tables", "xt_addrtype", "xt_comment", "xt_mark", "xt_multiport",
	"xt_nat", "xt_recent", "xt_statistic", "xt_tcpudp", "xt_conntrack",
	"xt_MASQUERADE", "xt_CT",

	"nf_reject_ipv4", "nf_reject_ipv6", "ipt_REJECT", "ip6t_REJECT",

	"nfnetlink", "nf_tables", "nft_compat", "nft_chain_nat", "nft_nat",
	"nft_masq", "nft_redir", "nft_ct",
	"nft_reject", "nft_reject_inet", "nft_reject_ipv4", "nft_reject_ipv6",
}
