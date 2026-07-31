//go:build darwin

package vz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	http    *http.Client
}

func newAgentClient(agentHostPort int) *agentClient {
	return &agentClient{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", agentHostPort),
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
