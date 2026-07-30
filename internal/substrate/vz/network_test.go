//go:build darwin

package vz

import "testing"

func TestFreeTCPPort_ReturnsAUsablePort(t *testing.T) {
	t.Parallel()

	port, err := freeTCPPort()
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}

	if port <= 0 || port > 65535 {
		t.Errorf("freeTCPPort = %d, want a valid TCP port number", port)
	}
}

func TestConnectedDatagramPair_ProducesConnectedEnds(t *testing.T) {
	t.Parallel()

	guestFile, hostConn, err := connectedDatagramPair()
	if err != nil {
		t.Fatalf("connectedDatagramPair: %v", err)
	}
	defer func() { _ = guestFile.Close() }()
	defer func() { _ = hostConn.Close() }()

	want := []byte("hello over the socketpair")

	if _, err := hostConn.Write(want); err != nil {
		t.Fatalf("writing from host side: %v", err)
	}

	got := make([]byte, len(want))
	if _, err := guestFile.Read(got); err != nil {
		t.Fatalf("reading from guest side: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("read %q, want %q", got, want)
	}
}
