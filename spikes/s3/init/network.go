package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/vishvananda/netlink"
)

// dhcpResult is what bringUpNetwork reports for the boot marker line.
type dhcpResult struct {
	iface string
	ip    net.IP
	gw    net.IP
	dns   []net.IP
}

// bringUpNetwork brings the first non-loopback link up, runs a DHCPv4
// DORA exchange against the host's VZNATNetworkDeviceAttachment (Apple's
// vmnet framework runs a real bootpd on the NAT subnet, so a static IP
// guess isn't reliable -- lease pools shift depending on how many other vz
// guests, e.g. colima, are already attached), and applies the offered
// address/route/DNS. No shell, no udhcpc: same "static Go PID 1" approach
// as the rest of rask-init.
func bringUpNetwork(ctx context.Context) (dhcpResult, error) {
	loLink, err := netlink.LinkByName("lo")
	if err == nil {
		_ = netlink.LinkSetUp(loLink)
	}

	link, err := firstEthernetLink()
	if err != nil {
		return dhcpResult{}, err
	}
	ifname := link.Attrs().Name

	if err := netlink.LinkSetUp(link); err != nil {
		return dhcpResult{}, fmt.Errorf("link set up %s: %w", ifname, err)
	}

	client, err := nclient4.New(ifname)
	if err != nil {
		return dhcpResult{}, fmt.Errorf("dhcp client on %s: %w", ifname, err)
	}
	defer client.Close()

	lease, err := client.Request(ctx)
	if err != nil {
		return dhcpResult{}, fmt.Errorf("dhcp request on %s: %w", ifname, err)
	}

	ip := lease.ACK.YourIPAddr
	mask := lease.ACK.SubnetMask()
	if len(mask) == 0 {
		mask = ip.DefaultMask() // fall back to the class-based default
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: mask}}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return dhcpResult{}, fmt.Errorf("addr add %s to %s: %w", addr, ifname, err)
	}

	routers := lease.ACK.Router()
	var gw net.IP
	if len(routers) > 0 {
		gw = routers[0]
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: gw}
		if err := netlink.RouteAdd(route); err != nil {
			return dhcpResult{}, fmt.Errorf("default route via %s on %s: %w", gw, ifname, err)
		}
	}

	dns := lease.ACK.DNS()
	if err := writeResolvConf(dns); err != nil {
		return dhcpResult{}, fmt.Errorf("write resolv.conf: %w", err)
	}

	return dhcpResult{iface: ifname, ip: ip, gw: gw, dns: dns}, nil
}

// firstEthernetLink returns the first non-loopback network link, polling
// briefly since virtio-net enumeration can lag a few milliseconds behind
// the console marker for mountBase.
func firstEthernetLink() (netlink.Link, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		links, err := netlink.LinkList()
		if err != nil {
			return nil, fmt.Errorf("link list: %w", err)
		}
		for _, l := range links {
			if l.Attrs().Flags&net.FlagLoopback == 0 {
				return l, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no non-loopback link found within %s", 5*time.Second)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// writeResolvConf writes DNS servers reported by DHCP option 6. If the
// lease didn't include any, falls back to public resolvers so image pulls
// can still resolve registry hostnames.
func writeResolvConf(dns []net.IP) error {
	if len(dns) == 0 {
		dns = []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("8.8.8.8")}
	}
	var content string
	for _, ip := range dns {
		content += fmt.Sprintf("nameserver %s\n", ip)
	}
	if err := os.MkdirAll("/etc", 0o755); err != nil {
		return err
	}
	return os.WriteFile("/etc/resolv.conf", []byte(content), 0o644)
}
