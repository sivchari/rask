//go:build linux

package main

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"

	"github.com/sivchari/rask/internal/cluster"
	"github.com/sivchari/rask/internal/guestagent"
)

// resolvConfPath and hostsPath are Linux's fixed, hardcoded lookup paths;
// programs that need them (containerd's CRI sandbox setup among them, see
// configureNetwork's doc comment) always read exactly these paths, never
// look them up through a config value.
const (
	resolvConfPath = "/etc/resolv.conf"
	hostsPath      = "/etc/hosts"
)

// Static network configuration matching the gvisor-tap-vsock subnet
// internal/substrate/vz's host side configures (the M0 s4 spike's
// production recipe: subnet 192.168.127.0/24, guest .2, gateway/DNS .1).
// guestagent.Addr must equal guestIP: it's the address the host's guest
// agent HTTP client connects to.
const (
	guestIP   = guestagent.Addr
	guestCIDR = guestIP + "/24"
	gatewayIP = "192.168.127.1"
	linkName  = "eth0"
)

// configureNetwork brings up loopback and eth0 with a static address in the
// gvisor-tap-vsock subnet, a default route via the gateway, and a
// resolv.conf pointing at the gateway's embedded DNS server. No DHCP: the
// host's gvisor-tap-vsock stack assigns a fixed guest address, so a DORA
// exchange would be pure overhead (the M0 s4 spike found the static IP
// path preferable to DHCP).
func configureNetwork() error {
	if loLink, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(loLink)
	}

	link, err := netlink.LinkByName(linkName)
	if err != nil {
		return fmt.Errorf("find link %s: %w", linkName, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("set link %s up: %w", linkName, err)
	}

	addr, err := netlink.ParseAddr(guestCIDR)
	if err != nil {
		return fmt.Errorf("parse addr %s: %w", guestCIDR, err)
	}

	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr %s to %s: %w", guestCIDR, linkName, err)
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(gatewayIP),
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("add default route via %s: %w", gatewayIP, err)
	}

	if err := os.MkdirAll("/etc", 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}

	resolvConf := fmt.Sprintf("nameserver %s\n", gatewayIP)
	if err := os.WriteFile(resolvConfPath, []byte(resolvConf), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}

	// containerd's CRI sandbox setup copies the host's own /etc/hosts into
	// every pod sandbox as a starting point (internal/cri/server/podsandbox
	// .setupSandboxFiles -> c.os.CopyFile(etcHosts, sandboxEtcHosts, ...) in
	// containerd v2.3.3) — it does not create one if missing, it only
	// copies. Without this file, every pod sandbox creation fails outright
	// ("failed to generate sandbox hosts file ...: open /etc/hosts: no such
	// file or directory"), which is what blocked CoreDNS/local-path-
	// provisioner from ever leaving ContainerCreating in earlier attempts.
	hosts := fmt.Sprintf("127.0.0.1 localhost\n::1 localhost\n%s %s\n", guestIP, cluster.NodeName)
	if err := os.WriteFile(hostsPath, []byte(hosts), 0o644); err != nil {
		return fmt.Errorf("write hosts: %w", err)
	}

	return nil
}
