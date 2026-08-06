//go:build darwin

package vz_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/substrate/vz"
)

func TestNew_ReturnsDistinctInstances(t *testing.T) {
	t.Parallel()

	a := vz.New(t.TempDir(), nil)
	b := vz.New(t.TempDir(), nil)

	if a == b {
		t.Error("New() returned the same instance twice, want independent instances")
	}
}

func TestRuntime_Stop_NoopWhenNotRunning(t *testing.T) {
	t.Parallel()

	r := vz.New(t.TempDir(), nil)

	if err := r.Stop(context.Background(), "never-started"); err != nil {
		t.Errorf("Stop on a never-started cluster = %v, want nil (idempotent no-op)", err)
	}
}

func TestRuntime_Delete_SucceedsWhenNotRunning(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir, nil)

	// Simulate a cluster directory left behind without a running
	// vm-host (e.g. after Stop already ran).
	clusterDir := filepath.Join(homeDir, "clusters", "dev")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	if err := r.Delete(context.Background(), "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(clusterDir); !os.IsNotExist(err) {
		t.Errorf("cluster dir still exists after Delete: err=%v", err)
	}
}

func TestRuntime_Delete_ErrorsWhenPIDFilePresent(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir, nil)

	clusterDir := filepath.Join(homeDir, "clusters", "dev")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("seeding cluster dir: %v", err)
	}

	// A real, currently-running PID (this test process's own) so
	// Delete's "still running" check has something plausible to find,
	// without needing a real vm-host process.
	if err := os.WriteFile(filepath.Join(clusterDir, "vm-host.pid"), []byte("1"), 0o600); err != nil {
		t.Fatalf("seeding pidfile: %v", err)
	}

	if err := r.Delete(context.Background(), "dev"); err == nil {
		t.Fatal("Delete with a pidfile present = nil error, want error (cluster still running)")
	}
}

// TestRuntime_Delete_TreatsMalformedPIDAsNotRunning guards against a real
// incident risk found live: a leftover pidfile containing "-1" (written
// externally, not by Runtime.Start, which always writes a real
// cmd.Process.Pid) must never reach syscall.Kill. POSIX kill(2) treats pid
// -1 as "every process the caller may signal" and pid 0 as "every process
// in the caller's process group" — feeding either into Stop's
// SIGTERM/SIGKILL calls would broadcast a signal far outside this one
// cluster's vm-host process.
func TestRuntime_Delete_TreatsMalformedPIDAsNotRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		pidContents string
	}{
		{name: "negative one", pidContents: "-1"},
		{name: "zero", pidContents: "0"},
		{name: "negative", pidContents: "-42"},
		{name: "empty", pidContents: ""},
		{name: "non-numeric", pidContents: "not-a-pid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			r := vz.New(homeDir, nil)

			clusterDir := filepath.Join(homeDir, "clusters", "dev")
			if err := os.MkdirAll(clusterDir, 0o755); err != nil {
				t.Fatalf("seeding cluster dir: %v", err)
			}

			if err := os.WriteFile(filepath.Join(clusterDir, "vm-host.pid"), []byte(tt.pidContents), 0o600); err != nil {
				t.Fatalf("seeding pidfile: %v", err)
			}

			// Stop must treat this as "not running" (a no-op), not
			// attempt to signal the malformed pid.
			if err := r.Stop(context.Background(), "dev"); err != nil {
				t.Errorf("Stop with pidfile content %q = %v, want nil", tt.pidContents, err)
			}

			// Delete must likewise proceed (not report "still
			// running") and actually remove the directory.
			if err := r.Delete(context.Background(), "dev"); err != nil {
				t.Fatalf("Delete with pidfile content %q = %v, want nil", tt.pidContents, err)
			}

			if _, err := os.Stat(clusterDir); !os.IsNotExist(err) {
				t.Errorf("cluster dir still exists after Delete: err=%v", err)
			}
		})
	}
}

func TestRuntime_ExecWriteFilePortForward_ErrorWhenNotRunning(t *testing.T) {
	t.Parallel()

	r := vz.New(t.TempDir(), nil)
	ctx := context.Background()

	if _, err := r.Exec(ctx, "never-started", io.Discard, "true"); err == nil {
		t.Error("Exec on a never-started cluster = nil error, want error")
	}

	if err := r.WriteFile(ctx, "never-started", "/etc/x", nil); err == nil {
		t.Error("WriteFile on a never-started cluster = nil error, want error")
	}

	if _, _, err := r.PortForward(ctx, "never-started", "127.0.0.1:0", "127.0.0.1:0"); err == nil {
		t.Error("PortForward on a never-started cluster = nil error, want error")
	}
}

// seedRunningCluster writes just enough of a cluster's on-disk state
// (data/vm-state.json — see state.go's vmState) for agentClientFor to treat
// name as running and point its agentClient at agentHostPort, without a
// real vm-host process behind it. Mirrors this file's existing pattern of
// seeding vm-host.pid directly for the Stop/Delete tests above.
func seedRunningCluster(t *testing.T, homeDir, name string, agentHostPort int) {
	t.Helper()

	dataDir := filepath.Join(homeDir, "clusters", name, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("seeding data dir: %v", err)
	}

	state := struct {
		PID           int `json:"pid"`
		HostPort      int `json:"hostPort"`
		AgentHostPort int `json:"agentHostPort"`
	}{PID: os.Getpid(), AgentHostPort: agentHostPort}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshaling vm state: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "vm-state.json"), data, 0o600); err != nil {
		t.Fatalf("seeding vm-state.json: %v", err)
	}
}

// fakeGuestAgentServer stands in for rask-init's guest control agent,
// serving only guestagent.PathTunnel with the exact handshake protocol
// described in that constant's doc comment (cmd/rask-init is linux-only and
// so not importable from this darwin-only package's tests): dial "addr",
// hijack, write a zero-body HTTP/1.1 200 response, then relay raw bytes.
// Returns the host TCP port it listens on, for seedRunningCluster's
// agentHostPort.
func fakeGuestAgentServer(t *testing.T) int {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("addr")

		upstream, err := net.Dial("tcp", addr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}
		defer func() { _ = upstream.Close() }()

		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)

			return
		}

		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if _, err := bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"); err != nil {
			return
		}

		if err := bufrw.Flush(); err != nil {
			return
		}

		var wg sync.WaitGroup

		wg.Add(2)

		go func() {
			defer wg.Done()
			_, _ = io.Copy(upstream, conn)
		}()

		go func() {
			defer wg.Done()
			_, _ = io.Copy(conn, upstream)
		}()

		wg.Wait()
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for fake guest agent: %v", err)
	}

	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	return l.Addr().(*net.TCPAddr).Port
}

// echoUpstream starts a TCP listener that echoes back whatever it reads
// from each connection, standing in for the workload (a Service or a Pod)
// PortForward's remoteAddr ultimately points at.
func echoUpstream(t *testing.T) net.Listener {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening upstream: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				buf := make([]byte, 64)

				n, _ := conn.Read(buf)
				_, _ = conn.Write(buf[:n])
			}()
		}
	}()

	return l
}

func TestRuntime_PortForward_BindsPortZeroAndRelaysData(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir, nil)

	agentPort := fakeGuestAgentServer(t)
	seedRunningCluster(t, homeDir, "dev", agentPort)

	upstream := echoUpstream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localAddr, errCh, err := r.PortForward(ctx, "dev", "127.0.0.1:0", upstream.Addr().String())
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}

	if _, port, err := net.SplitHostPort(localAddr); err != nil || port == "0" {
		t.Fatalf("PortForward bound addr = %q, want a concrete resolved port", localAddr)
	}

	conn, err := net.Dial("tcp", localAddr)
	if err != nil {
		t.Fatalf("dialing forwarded port: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("writing to forwarded conn: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("reading echoed data: %v", err)
	}

	if string(buf) != "ping" {
		t.Errorf("echoed data = %q, want %q", buf, "ping")
	}

	cancel()

	// PortForward's accept loop closes the listener (deferred, so it runs
	// before errCh is closed) before returning, so errCh closing is
	// already proof the port was released — not re-verified here by
	// re-dialing localAddr, which would be racy under a parallel test
	// suite: another test's own ":0" listener can coincidentally reuse
	// the just-freed ephemeral port in that window (the same TOCTOU
	// network.go's freeTCPPort already documents as accepted elsewhere in
	// this package).
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("errCh did not close within 2s of ctx cancellation")
	}
}

func TestRuntime_PortForward_SecondForwardAfterCancelSucceeds(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir, nil)

	agentPort := fakeGuestAgentServer(t)
	seedRunningCluster(t, homeDir, "dev", agentPort)

	upstream := echoUpstream(t)

	ctx1, cancel1 := context.WithCancel(context.Background())

	addr1, errCh1, err := r.PortForward(ctx1, "dev", "127.0.0.1:0", upstream.Addr().String())
	if err != nil {
		t.Fatalf("first PortForward: %v", err)
	}

	cancel1()

	select {
	case <-errCh1:
	case <-time.After(2 * time.Second):
		t.Fatal("first errCh did not close within 2s of cancellation")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	addr2, _, err := r.PortForward(ctx2, "dev", "127.0.0.1:0", upstream.Addr().String())
	if err != nil {
		t.Fatalf("second PortForward after the first was canceled: %v", err)
	}

	conn, err := net.Dial("tcp", addr2)
	if err != nil {
		t.Fatalf("dialing second forwarded port %s (first was %s): %v", addr2, addr1, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("writing to second forwarded conn: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("reading echoed data on second forward: %v", err)
	}

	if string(buf) != "ping" {
		t.Errorf("echoed data = %q, want %q", buf, "ping")
	}
}

func TestRuntime_PortForward_UpstreamUnreachableStillClosesCleanlyOnCancel(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	r := vz.New(homeDir, nil)

	agentPort := fakeGuestAgentServer(t)
	seedRunningCluster(t, homeDir, "dev", agentPort)

	// Nothing listens on this loopback port: every accepted local
	// connection's tunnel attempt fails guest-side, exercising
	// relayThroughGuest's error path without hanging or leaking.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving an unused port: %v", err)
	}

	unreachable := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing reserved listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localAddr, errCh, err := r.PortForward(ctx, "dev", "127.0.0.1:0", unreachable)
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}

	conn, err := net.Dial("tcp", localAddr)
	if err != nil {
		t.Fatalf("dialing forwarded port: %v", err)
	}

	// The local connection should be closed by the failed tunnel attempt
	// without the harness needing to write anything.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("reading from a conn whose tunnel dial failed = nil error, want the conn closed")
	}

	_ = conn.Close()

	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("errCh did not close within 2s of ctx cancellation")
	}
}
