// Package guestagent is the wire protocol shared between rask-init's guest
// control agent (cmd/rask-init, running inside a vz VM) and the host-side
// client that talks to it (internal/substrate/vz). It has no OS
// dependencies of its own, so it builds and tests on any host.
//
// The vz guest has no shared filesystem with the host (unlike hostproc,
// which runs directly on the host — see internal/substrate/hostproc's
// package doc): the per-cluster data disk is a virtio-blk block device the
// host cannot read while the guest owns it. rask-init exposes a minimal
// HTTP control surface over the same gvisor-tap-vsock network link already
// used for the forwarded API server port, so the host can retrieve the
// admin kubeconfig once boot succeeds, and so substrate.Runtime's
// Exec/WriteFile methods have something real to talk to.
package guestagent

import (
	"fmt"
	"net/url"
)

// Addr and Port are where rask-init's guest agent listens, on the static
// guest IP the vz substrate's gvisor-tap-vsock network assigns (see
// internal/substrate/vz's network configuration, matching the M0 s4
// spike's subnet).
const (
	Addr = "192.168.127.2"
	Port = 7777
)

// HTTP paths the guest agent serves.
const (
	// PathHealthz returns 200 once rask-init's bootstrap.Boot call has
	// returned successfully (node Ready).
	PathHealthz = "/healthz"

	// PathKubeconfig returns the raw bytes of the cluster's admin
	// kubeconfig (server URL still "https://127.0.0.1:6443", the
	// guest-internal loopback address — the host client rewrites it to
	// the forwarded host port before writing it to disk).
	PathKubeconfig = "/kubeconfig"

	// PathTimeline returns a plain-text phase-by-phase boot latency
	// breakdown (internal/bootstrap.Timeline.Breakdown, rendered the same
	// way internal/substrate/hostproc's writeTimeline does), for
	// "rask create --verbose" to print. Only available once boot
	// succeeds.
	PathTimeline = "/timeline"

	// PathExec accepts a POST with an ExecRequest JSON body, runs it, and
	// streams the command's combined stdout/stderr as the chunked
	// response body. The exit code is returned in the ExitCodeTrailer
	// HTTP trailer, readable only after the body has been fully drained.
	PathExec = "/exec"

	// PathFile accepts a PUT whose query parameter "path" (see
	// EncodeFilePath/DecodeFilePath) names the destination and whose body
	// is the raw file content, or a GET with the same "path" parameter to
	// read a file back (e.g. a component's log file under
	// guestlayout.GuestAgentDataDir/logs, otherwise unreachable from the
	// host — there is no shell in this guest to cat/tail anything, and no
	// shared filesystem the way internal/substrate/hostproc has).
	PathFile = "/file"
)

// ExitCodeTrailer is the HTTP trailer PathExec's response sets to the
// executed command's exit code (as a base-10 integer). The server must
// declare it via the "Trailer" header before writing any body bytes, and
// the client must fully read the body (io.Copy to EOF) before the trailer
// is populated — both are standard net/http chunked-trailer behavior, not
// anything guestagent-specific.
const ExitCodeTrailer = "X-Rask-Exit-Code"

// ExecRequest is PathExec's JSON request body.
type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// EncodeFilePath renders path as a PathFile query parameter value.
func EncodeFilePath(path string) string {
	return url.QueryEscape(path)
}

// DecodeFilePath is EncodeFilePath's inverse.
func DecodeFilePath(encoded string) (string, error) {
	path, err := url.QueryUnescape(encoded)
	if err != nil {
		return "", fmt.Errorf("guestagent: decoding file path %q: %w", encoded, err)
	}

	return path, nil
}
