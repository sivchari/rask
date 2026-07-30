//go:build linux

package hostproc

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntime_ExecUnknownClusterErrors(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	var buf bytes.Buffer

	if _, err := r.Exec(context.Background(), "nonexistent", &buf, "/bin/echo", "hi"); err == nil {
		t.Fatal("Exec on nonexistent cluster = nil error, want error")
	}
}

func TestRuntime_ExecStreamsStdoutAndReturnsExitCode(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	if err := os.MkdirAll(r.dataDir("dev"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var buf bytes.Buffer

	code, err := r.Exec(context.Background(), "dev", &buf, "/bin/echo", "hello")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if got := buf.String(); got != "hello\n" {
		t.Errorf("stdout = %q, want %q", got, "hello\n")
	}
}

func TestRuntime_ExecReturnsNonZeroExitCode(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	if err := os.MkdirAll(r.dataDir("dev"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var buf bytes.Buffer

	code, err := r.Exec(context.Background(), "dev", &buf, "/bin/sh", "-c", "exit 7")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestRuntime_WriteFileCreatesParentDirsAndContent(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	if err := os.MkdirAll(r.dataDir("dev"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	target := filepath.Join(t.TempDir(), "nested", "file.txt")

	if err := r.WriteFile(context.Background(), "dev", target, []byte("content")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}

	if string(got) != "content" {
		t.Errorf("content = %q, want %q", got, "content")
	}
}

func TestRuntime_PortForwardRelaysData(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir())

	if err := os.MkdirAll(r.dataDir("dev"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening upstream: %v", err)
	}
	defer func() { _ = upstream.Close() }()

	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		buf := make([]byte, 64)

		n, _ := conn.Read(buf)
		_, _ = conn.Write(buf[:n])
	}()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving local addr: %v", err)
	}

	localAddr := local.Addr().String()

	if err := local.Close(); err != nil {
		t.Fatalf("closing reserved listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh, err := r.PortForward(ctx, "dev", localAddr, upstream.Addr().String())
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}

	var conn net.Conn

	for i := 0; i < 50; i++ {
		conn, err = net.Dial("tcp", localAddr)
		if err == nil {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err != nil {
		t.Fatalf("dialing forwarded port: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("writing to forwarded conn: %v", err)
	}

	buf := make([]byte, 4)

	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("reading echoed data: %v", err)
	}

	if string(buf) != "ping" {
		t.Errorf("echoed data = %q, want %q", buf, "ping")
	}

	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Error("errCh did not close within 2s of ctx cancellation")
	}
}
