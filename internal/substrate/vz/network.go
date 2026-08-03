//go:build darwin

package vz

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"

	cvz "github.com/Code-Hex/vz/v3"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/sirupsen/logrus"

	"github.com/sivchari/rask/internal/guestagent"
)

// Network subnet/address constants, matching cmd/rask-init's
// network.go/rosetta.go and the M0 s4 spike's production recipe exactly:
// both sides of the host/guest boundary must agree on these.
const (
	networkSubnet            = "192.168.127.0/24"
	networkGatewayIP         = "192.168.127.1"
	networkGatewayMacAddress = "5a:94:ef:e4:0c:dd"

	// networkGuestIP must equal guestagent.Addr and cmd/rask-init's
	// network.go guestIP constant.
	networkGuestIP = guestagent.Addr

	apiserverGuestPort = 6443
)

func init() {
	// gvisor-tap-vsock logs an expected, benign error via its
	// package-level logrus logger every time a socketpair connection is
	// closed intentionally (AcceptVfkit's shutdown path — a "vz API
	// surprise" found during the M0 s4 spike). Silencing below Error
	// keeps rask's own logs free of noise from routine VM teardown;
	// genuine wiring failures still surface through this package's own
	// returned errors, not through gvisor-tap-vsock's logger.
	logrus.SetLevel(logrus.ErrorLevel)
}

// clusterNetwork owns one cluster VM's gvisor-tap-vsock virtual network and
// the socketpair wiring it into vz. Created once per Start, torn down on
// Stop.
type clusterNetwork struct {
	vn            *virtualnetwork.VirtualNetwork
	guestFile     *os.File
	hostConn      net.Conn
	acceptDone    chan struct{}
	hostPort      int // forwards to the guest's apiserver port
	agentHostPort int // forwards to the guest's rask-init control agent
}

// newClusterNetwork picks two free host ports, builds a gvisor-tap-vsock
// virtual network forwarding one to the guest's apiserver port and the
// other to rask-init's control agent (internal/guestagent), and wires a
// connected AF_UNIX SOCK_DGRAM socketpair between it and a vz
// FileHandleNetworkDeviceAttachment — the exact recipe the M0 s4 spike
// validated end to end (extended here with a second Forwards entry for the
// guest agent, which S4 didn't need).
func newClusterNetwork() (*clusterNetwork, error) {
	hostPort, err := freeTCPPort()
	if err != nil {
		return nil, fmt.Errorf("vz: picking a free host port: %w", err)
	}

	agentHostPort, err := freeTCPPort()
	if err != nil {
		return nil, fmt.Errorf("vz: picking a free host port for the guest agent: %w", err)
	}

	cfg := &types.Configuration{
		MTU:               1500,
		Subnet:            networkSubnet,
		GatewayIP:         networkGatewayIP,
		GatewayMacAddress: networkGatewayMacAddress,
		Forwards: map[string]string{
			fmt.Sprintf("127.0.0.1:%d", hostPort):      fmt.Sprintf("%s:%d", networkGuestIP, apiserverGuestPort),
			fmt.Sprintf("127.0.0.1:%d", agentHostPort): fmt.Sprintf("%s:%d", networkGuestIP, guestagent.Port),
		},
	}

	vn, err := virtualnetwork.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("vz: creating virtual network: %w", err)
	}

	guestFile, hostConn, err := connectedDatagramPair()
	if err != nil {
		return nil, fmt.Errorf("vz: creating host<->guest network socketpair: %w", err)
	}

	n := &clusterNetwork{vn: vn, guestFile: guestFile, hostConn: hostConn, hostPort: hostPort, agentHostPort: agentHostPort, acceptDone: make(chan struct{})}

	go func() {
		defer close(n.acceptDone)

		// AcceptVfkit blocks until hostConn is closed (Close), which is
		// the only reliable way to stop it — a "vz API surprise" found
		// during the M0 s4 spike — so its returned error on shutdown is
		// expected and intentionally ignored here.
		_ = n.vn.AcceptVfkit(context.Background(), n.hostConn)
	}()

	return n, nil
}

// networkDeviceConfig returns the vz network device configuration to attach
// to a VirtualMachineConfiguration, wired to this cluster's gvisor-tap-vsock
// network via n.guestFile.
func (n *clusterNetwork) networkDeviceConfig() (*cvz.VirtioNetworkDeviceConfiguration, error) {
	mac, err := cvz.NewRandomLocallyAdministeredMACAddress()
	if err != nil {
		return nil, fmt.Errorf("vz: generating MAC address: %w", err)
	}

	attachment, err := cvz.NewFileHandleNetworkDeviceAttachment(n.guestFile)
	if err != nil {
		return nil, fmt.Errorf("vz: network device attachment: %w", err)
	}

	cfg, err := cvz.NewVirtioNetworkDeviceConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("vz: network device configuration: %w", err)
	}

	cfg.SetMACAddress(mac)

	return cfg, nil
}

// HostPort is the host TCP port 127.0.0.1:<HostPort> forwards to the
// guest's apiserver port.
func (n *clusterNetwork) HostPort() int {
	return n.hostPort
}

// AgentHostPort is the host TCP port 127.0.0.1:<AgentHostPort> forwards to
// rask-init's control agent (internal/guestagent).
func (n *clusterNetwork) AgentHostPort() int {
	return n.agentHostPort
}

// Close closes the host<->guest socketpair, unblocking AcceptVfkit (see
// newClusterNetwork's goroutine), and waits for it to exit.
func (n *clusterNetwork) Close() {
	_ = n.hostConn.Close()
	_ = n.guestFile.Close()
	<-n.acceptDone
}

// freeTCPPort binds an ephemeral TCP port on 127.0.0.1, closes it, and
// returns the port number. A bind-then-close-then-reuse pattern with an
// inherent (accepted, per the M0 s4 spike) TOCTOU race against other local
// processes racing for the same port between the close here and
// gvisor-tap-vsock's own bind inside virtualnetwork.New.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("vz: unexpected listener addr type %T", l.Addr())
	}

	return addr.Port, nil
}

// connectedDatagramPair creates a SOCK_DGRAM AF_UNIX socketpair: the first
// returned file is handed to vz as the guest side of the virtio-net device,
// the second is wrapped as a net.Conn for gvisor-tap-vsock's AcceptVfkit —
// the M0 s4 spike's wiring, which needs no adapter code (vz's
// FileHandleNetworkDeviceAttachment and gvisor-tap-vsock's vfkit protocol
// both expect raw, unframed datagrams per read/write).
func connectedDatagramPair() (guestFile *os.File, hostConn net.Conn, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("socketpair: %w", err)
	}

	guestFile = os.NewFile(uintptr(fds[0]), "vz-guest-net")
	hostFile := os.NewFile(uintptr(fds[1]), "gvisor-host-net")
	defer func() { _ = hostFile.Close() }()

	hostConn, err = net.FileConn(hostFile)
	if err != nil {
		_ = guestFile.Close()

		return nil, nil, fmt.Errorf("net.FileConn: %w", err)
	}

	return guestFile, hostConn, nil
}
