//go:build linux

package main

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"

	"github.com/sivchari/rask/internal/guestagent"
)

// Static network configuration matching the gvisor-tap-vsock subnet
// internal/substrate/vz's host side configures (spikes/s4/RESULTS.md's
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
// exchange would be pure overhead (see spikes/s4/RESULTS.md, "static IP
// path was used rather than DHCP").
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
	if err := os.WriteFile("/etc/resolv.conf", []byte(resolvConf), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}

	return nil
}
