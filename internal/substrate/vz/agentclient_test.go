//go:build darwin

package vz

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sivchari/rask/internal/guestagent"
)

// newTestAgentClient points an agentClient at an httptest server instead of
// a real forwarded gvisor-tap-vsock port — agentClient is plain net/http
// against a base URL, so this exercises the exact same code path a real
// vz guest would.
func newTestAgentClient(t *testing.T, handler http.Handler) *agentClient {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing test server port: %v", err)
	}

	return newAgentClient(port)
}

func TestAgentClient_WaitHealthy_SucceedsOnceHealthy(t *testing.T) {
	t.Parallel()

	var healthy atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	})

	c := newTestAgentClient(t, mux)

	go func() {
		time.Sleep(30 * time.Millisecond)
		healthy.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
}

func TestAgentClient_WaitHealthy_TimesOut(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	c := newTestAgentClient(t, mux)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := c.WaitHealthy(ctx); err == nil {
		t.Fatal("WaitHealthy against a permanently-unhealthy server = nil error, want error")
	}
}

func TestAgentClient_Kubeconfig(t *testing.T) {
	t.Parallel()

	want := []byte("apiVersion: v1\nkind: Config\n")

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathKubeconfig, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	})

	c := newTestAgentClient(t, mux)

	got, err := c.Kubeconfig(context.Background())
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("Kubeconfig = %q, want %q", got, want)
	}
}

func TestAgentClient_Exec_StreamsOutputAndReturnsExitCode(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathExec, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"echo"`) {
			t.Errorf("request body = %s, want it to contain the command", body)
		}

		w.Header().Set("Trailer", guestagent.ExitCodeTrailer)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from guest\n"))
		w.Header().Set(guestagent.ExitCodeTrailer, "0")
	})

	c := newTestAgentClient(t, mux)

	var stdout bytes.Buffer

	exitCode, err := c.Exec(context.Background(), &stdout, "echo", []string{"hi"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}

	if stdout.String() != "hello from guest\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello from guest\n")
	}
}

func TestAgentClient_Exec_NonZeroExitCode(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathExec, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", guestagent.ExitCodeTrailer)
		w.WriteHeader(http.StatusOK)
		w.Header().Set(guestagent.ExitCodeTrailer, "17")
	})

	c := newTestAgentClient(t, mux)

	exitCode, err := c.Exec(context.Background(), io.Discard, "false", nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if exitCode != 17 {
		t.Errorf("exitCode = %d, want 17", exitCode)
	}
}

func TestAgentClient_WriteFile(t *testing.T) {
	t.Parallel()

	var gotPath string

	var gotBody []byte

	mux := http.NewServeMux()
	mux.HandleFunc(guestagent.PathFile, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}

		var err error

		gotPath, err = guestagent.DecodeFilePath(r.URL.Query().Get("path"))
		if err != nil {
			t.Errorf("DecodeFilePath: %v", err)
		}

		gotBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusOK)
	})

	c := newTestAgentClient(t, mux)

	if err := c.WriteFile(context.Background(), "/etc/rask/hello", []byte("payload")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if gotPath != "/etc/rask/hello" {
		t.Errorf("server saw path = %q, want %q", gotPath, "/etc/rask/hello")
	}

	if string(gotBody) != "payload" {
		t.Errorf("server saw body = %q, want %q", gotBody, "payload")
	}
}
