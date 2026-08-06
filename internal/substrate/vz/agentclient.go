//go:build darwin

package vz

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sivchari/rask/internal/guestagent"
)

// agentClient talks to rask-init's guest control agent through the
// gvisor-tap-vsock port forward for it (clusterNetwork.AgentHostPort), not
// directly to the guest's own address: the host has no route into the
// gvisor userspace network stack other than the forwards configured at
// virtualnetwork.New time (see network.go).
type agentClient struct {
	baseURL string
	addr    string
	http    *http.Client
}

func newAgentClient(agentHostPort int) *agentClient {
	addr := fmt.Sprintf("127.0.0.1:%d", agentHostPort)

	return &agentClient{
		baseURL: "http://" + addr,
		addr:    addr,
		http:    &http.Client{},
	}
}

// WaitHealthy polls PathHealthz until it returns 200 or ctx is done.
func (c *agentClient) WaitHealthy(ctx context.Context) error {
	url := c.baseURL + guestagent.PathHealthz

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := c.http.Do(req)
			if err == nil {
				_ = resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("vz: waiting for guest agent to become healthy: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Kubeconfig fetches the cluster's admin kubeconfig (server URL still the
// guest-internal "https://127.0.0.1:6443" — the caller rewrites it).
func (c *agentClient) Kubeconfig(ctx context.Context) ([]byte, error) {
	return c.get(ctx, guestagent.PathKubeconfig)
}

// Timeline fetches the plain-text boot phase breakdown.
func (c *agentClient) Timeline(ctx context.Context) ([]byte, error) {
	return c.get(ctx, guestagent.PathTimeline)
}

func (c *agentClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vz: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vz: reading response body for %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vz: GET %s: status %s: %s", path, resp.Status, data)
	}

	return data, nil
}

// Exec runs command with args inside the guest, streaming its combined
// stdout/stderr to stdout, and returns its exit code.
func (c *agentClient) Exec(ctx context.Context, stdout io.Writer, command string, args []string) (int, error) {
	body, err := json.Marshal(guestagent.ExecRequest{Command: command, Args: args})
	if err != nil {
		return 0, fmt.Errorf("vz: encoding exec request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+guestagent.PathExec, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vz: POST %s: %w", guestagent.PathExec, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)

		return 0, fmt.Errorf("vz: exec %s: status %s: %s", command, resp.Status, data)
	}

	if _, err := io.Copy(stdout, resp.Body); err != nil {
		return 0, fmt.Errorf("vz: streaming exec output: %w", err)
	}

	exitCode, err := strconv.Atoi(resp.Trailer.Get(guestagent.ExitCodeTrailer))
	if err != nil {
		return 0, fmt.Errorf("vz: exec response missing/invalid %s trailer: %w", guestagent.ExitCodeTrailer, err)
	}

	return exitCode, nil
}

// WriteFile writes data to path inside the guest.
func (c *agentClient) WriteFile(ctx context.Context, path string, data []byte) error {
	encodedPath, err := url.Parse(c.baseURL + guestagent.PathFile)
	if err != nil {
		return err
	}

	q := encodedPath.Query()
	q.Set("path", guestagent.EncodeFilePath(path))
	encodedPath.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, encodedPath.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vz: PUT %s: %w", guestagent.PathFile, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("vz: writing %s: status %s: %s", path, resp.Status, data)
	}

	return nil
}

// ReadFile reads path back out of the guest — the only way to inspect a
// file (e.g. a component's log under guestlayout.GuestAgentDataDir/logs)
// there is no shell in the guest to cat/tail, and the data disk isn't
// shared with the host.
func (c *agentClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	u, err := url.Parse(c.baseURL + guestagent.PathFile)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("path", guestagent.EncodeFilePath(path))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vz: GET %s: %w", guestagent.PathFile, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vz: reading response body for %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vz: reading %s: status %s: %s", path, resp.Status, data)
	}

	return data, nil
}

// Tunnel opens a connection to remoteAddr from inside the guest (see
// guestagent.PathTunnel's doc comment for why this has to be dialed
// guest-side rather than through the gvisor-tap-vsock link directly) and
// returns it as a net.Conn the caller can relay bytes over until it closes
// it or ctx is canceled.
//
// This dials c.addr directly (bypassing c.http, which has no API for
// handing back a hijacked connection) because the guest agent's response
// to a successful request is not an ordinary HTTP response: the request is
// written as plain HTTP so PathTunnel's http.ServeMux still routes it
// normally, but the guest then hijacks the connection and, from that point
// on, an initial handshake response is followed immediately by raw relayed
// bytes with no further HTTP framing (see PathTunnel's doc comment).
//
// The handshake response is nonetheless a complete, valid, zero-body HTTP
// response (not a bespoke sentinel), read with the standard
// http.ReadResponse against the same bufio.Reader that keeps being used
// for every read afterward (tunnelConn) — required so any tunnel bytes the
// guest already sent in the same TCP segment as the handshake, and that
// therefore already landed in that Reader's internal buffer, aren't lost by
// switching to reading net.Conn directly once the handshake is parsed.
func (c *agentClient) Tunnel(ctx context.Context, remoteAddr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("vz: dialing guest agent for tunnel: %w", err)
	}

	u := c.baseURL + guestagent.PathTunnel + "?" + url.Values{"addr": {remoteAddr}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	if err := req.Write(conn); err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("vz: writing tunnel request: %w", err)
	}

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("vz: reading tunnel handshake: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		_ = conn.Close()

		return nil, fmt.Errorf("vz: tunnel to %s: status %s: %s", remoteAddr, resp.Status, data)
	}

	return &tunnelConn{Conn: conn, r: br}, nil
}

// tunnelConn is agentClient.Tunnel's returned net.Conn: reads go through r
// (the same bufio.Reader the handshake response was parsed from, so any
// tunnel bytes it already buffered are not lost — see Tunnel's doc
// comment), writes go straight to the underlying connection since nothing
// buffers those client-side.
type tunnelConn struct {
	net.Conn
	r *bufio.Reader
}

func (t *tunnelConn) Read(p []byte) (int, error) {
	return t.r.Read(p)
}
