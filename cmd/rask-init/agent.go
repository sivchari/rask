//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sivchari/rask/internal/bootstrap"
	"github.com/sivchari/rask/internal/guestagent"
)

// serveAgent runs rask-init's guest control agent forever (this is the
// last thing main does; it never returns while the guest is up). result is
// nil if runBoot failed: PathHealthz still answers (with an error), so the
// host's boot-readiness poll gets a definite response instead of a
// connection that never comes up at all.
func serveAgent(result *bootstrap.Result) {
	mux := http.NewServeMux()

	mux.HandleFunc(guestagent.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		if result == nil {
			http.Error(w, "boot failed", http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc(guestagent.PathKubeconfig, func(w http.ResponseWriter, r *http.Request) {
		handleKubeconfig(w, r, result)
	})

	mux.HandleFunc(guestagent.PathTimeline, func(w http.ResponseWriter, r *http.Request) {
		handleTimeline(w, r, result)
	})

	mux.HandleFunc(guestagent.PathExec, handleExec)
	mux.HandleFunc(guestagent.PathFile, handleFile)
	mux.HandleFunc(guestagent.PathTunnel, handleTunnel)

	addr := fmt.Sprintf("%s:%d", guestagent.Addr, guestagent.Port)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		fmt.Printf("RASK-INIT-AGENT-FAILED err=%v\n", err)
	}
}

func handleKubeconfig(w http.ResponseWriter, _ *http.Request, result *bootstrap.Result) {
	if result == nil {
		http.Error(w, "boot failed, no kubeconfig available", http.StatusServiceUnavailable)

		return
	}

	data, err := os.ReadFile(result.AdminKubeconfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func handleTimeline(w http.ResponseWriter, _ *http.Request, result *bootstrap.Result) {
	if result == nil {
		http.Error(w, "boot failed, no timeline available", http.StatusServiceUnavailable)

		return
	}

	var buf bytes.Buffer

	fmt.Fprintf(&buf, "%-20s %12s %12s\n", "PHASE", "CUMULATIVE", "DELTA")

	for _, e := range result.Timeline.Breakdown() {
		fmt.Fprintf(&buf, "%-20s %12s %12s\n", e.Name, e.Elapsed.Round(time.Millisecond), e.SincePrev.Round(time.Millisecond))
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// handleExec runs the requested command directly on the guest (rask-init's
// own PID 1 process is the only "isolation boundary" here, matching
// internal/substrate/hostproc.Exec's documented v1 scope) and streams its
// combined stdout/stderr as the chunked response body, reporting the exit
// code via guestagent.ExitCodeTrailer once the body is fully written.
func handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var req guestagent.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decoding request: %v", err), http.StatusBadRequest)

		return
	}

	w.Header().Set("Trailer", guestagent.ExitCodeTrailer)
	w.WriteHeader(http.StatusOK)

	cmd := exec.CommandContext(r.Context(), req.Command, req.Args...)
	cmd.Stdout = w
	cmd.Stderr = w

	exitCode := 0

	if err := cmd.Run(); err != nil {
		exitCode = exitCodeFromError(err)
	}

	w.Header().Set(guestagent.ExitCodeTrailer, strconv.Itoa(exitCode))
}

func exitCodeFromError(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}

// handleFile writes the request body to (PUT) or returns the contents of
// (GET) the path named by the "path" query parameter
// (guestagent.EncodeFilePath) — the guest-side counterpart of
// substrate.Runtime.WriteFile, and (for GET) the only way to read a file
// out of this guest at all: there is no shell to cat/tail anything, and no
// filesystem shared with the host.
func handleFile(w http.ResponseWriter, r *http.Request) {
	path, err := guestagent.DecodeFilePath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	case http.MethodPut:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		f, err := os.Create(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}
		defer func() { _ = f.Close() }()

		if _, err := io.Copy(f, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTunnel is the guest-side counterpart of
// internal/substrate/vz/agentclient.go's Tunnel: it dials the "addr" query
// parameter from inside this guest's own network namespace (see
// guestagent.PathTunnel's doc comment for why that matters — it is the only
// vantage point that can reach a pod or Service IP, unlike the
// gvisor-tap-vsock link itself) and, once connected, relays raw bytes
// between that connection and the caller's until either side closes or
// r.Context() is done (canceled once ServeHTTP returns, which here only
// happens after the relay itself finishes).
func handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	addr := r.URL.Query().Get("addr")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		http.Error(w, fmt.Sprintf("invalid addr %q: %v", addr, err), http.StatusBadRequest)

		return
	}

	upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", addr)
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

	// A fully framed, zero-body HTTP response (not the raw "OK" sentinel
	// gvisor-tap-vsock's own /tunnel endpoint uses internally) so the
	// client can parse it with net/http's own http.ReadResponse before
	// reusing the same buffered connection for the raw relay below —
	// see agentClient.Tunnel's doc comment for why that ordering matters.
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
}
