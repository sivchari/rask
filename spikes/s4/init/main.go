// Command rask-init is the minimal PID 1 for spike S4's guest.
//
// It builds on spike S2's rask-init (mount base pseudo-filesystems, print a
// console marker, reap zombies) and adds: static IPv4 configuration of eth0
// against the gvisor-tap-vsock gateway, a stand-in "apiserver" HTTP server
// on :6443 for the host to reach through the port forward, and a one-shot
// outbound HTTP probe through the gateway's NAT to prove internet egress.
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
)

const (
	apiserverAddr = ":6443"
	apiserverBody = "rask-spike-s4-guest-ok\n"

	guestIP     = "192.168.127.2"
	guestCIDR   = guestIP + "/24"
	gatewayIP   = "192.168.127.1"
	linkName    = "eth0"
	outboundURL = "http://example.com"
)

func mount(source, target, fstype string, flags uintptr) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Printf("rask-init: mkdir %s: %v\n", target, err)
		return
	}
	if err := syscall.Mount(source, target, fstype, flags, ""); err != nil {
		fmt.Printf("rask-init: mount %s on %s: %v\n", source, target, err)
	}
}

// configureNetwork brings up eth0 with a static address in the
// gvisor-tap-vsock subnet, a default route via the gateway, and a
// resolv.conf pointing at the gateway's embedded DNS server.
func configureNetwork() error {
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

// serveAPIserverStandIn starts the stand-in "apiserver" HTTP server that
// the host reaches through the gvisor-tap-vsock port forward.
func serveAPIserverStandIn() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, apiserverBody)
	})
	server := &http.Server{
		Addr:              apiserverAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		fmt.Printf("rask-init: apiserver stand-in exited: %v\n", err)
	}
}

// probeOutbound makes one best-effort HTTP GET through the gateway's NAT to
// prove guest->internet egress, retrying briefly since the host-side NAT
// path and DNS may not be ready in the first moments after boot.
func probeOutbound() {
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		resp, err := client.Get(outboundURL)
		if err == nil {
			_ = resp.Body.Close()
			fmt.Printf("RASK-OUTBOUND-RESULT status=%d attempt=%d\n", resp.StatusCode, attempt)
			return
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	fmt.Printf("RASK-OUTBOUND-RESULT status=0 attempt=5 err=%v\n", lastErr)
}

func main() {
	start := time.Now()

	mount("proc", "/proc", "proc", 0)
	mount("sysfs", "/sys", "sysfs", 0)
	mount("devtmpfs", "/dev", "devtmpfs", 0)

	fmt.Printf("RASK-INIT-BOOT-COMPLETE t=%s unixnano=%d\n", start.Format(time.RFC3339Nano), start.UnixNano())

	if err := configureNetwork(); err != nil {
		fmt.Printf("RASK-INIT-NET-FAILED err=%v\n", err)
	} else {
		fmt.Printf("RASK-INIT-NET-READY t=%s\n", time.Now().Format(time.RFC3339Nano))
	}

	go serveAPIserverStandIn()
	go probeOutbound()

	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD)

	for range sigs {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}
