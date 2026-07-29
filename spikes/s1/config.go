package main

import (
	"encoding/base64"
	"fmt"
	"net"
)

// Cluster-wide constants. These are fixed (not flag-configurable) because
// the PKI SANs, service CIDR and kubeconfigs are generated against them.
const (
	nodeName              = "rask-node"
	apiserverPort         = 6443
	kubeletPort           = 10250
	kubeletHealthzPort    = 10248
	serviceClusterIPRange = "10.96.0.0/16"
	podCIDR               = "10.244.0.0/16"
	clusterName           = "rask-spike-s1"
)

func base64Std(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// detectOutboundIP returns the local IP address the kernel would use to
// reach the internet, without sending any packets (UDP "connect" just
// selects a route). Used as the kube-apiserver advertise address / SAN and
// as the kubelet --node-ip.
func detectOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("detecting outbound IP: %w", err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("unexpected local addr type %T", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}
