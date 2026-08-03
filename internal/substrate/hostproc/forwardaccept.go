//go:build linux

package hostproc

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/sivchari/rask/internal/cluster"
)

// ensureForwardAcceptPodCIDR guarantees the host's iptables FORWARD chain
// accepts traffic to and from the cluster's pod CIDR before any pod
// traffic can flow. Docker, when installed on the host, sets FORWARD's
// default policy to DROP and only ever adds explicit ACCEPT rules scoped
// to its own bridges — never for rask's cni0 — so bridged pod-to-pod and
// pod-to-ClusterIP traffic is silently dropped at the host's FORWARD chain
// even though "rask create" itself reports success. Found live: fjord's CI
// smoke E2E, running rask directly on a GitHub-hosted ubuntu runner's host
// netns (Docker installed there by default), created the cluster fine but
// every in-cluster call failed ("Could not connect to the endpoint URL
// http://fjord-agent.kube-system.svc:8080"); the identical fjord version
// run inside a privileged container — where rask owns the container's own
// netns and Docker's host FORWARD rules never apply — passed the same
// probes (DNS, pod->podIP, pod->ClusterIP, pod->kubernetes Service),
// proving the Service path itself was fine and only the host FORWARD
// policy differed. k3s and kind both hit the identical problem and both
// work around it the same way: insert explicit ACCEPT rules for the pod
// CIDR at the top of FORWARD. The vz substrate never needs this — its
// cluster runs inside its own VM, where Docker's host-level FORWARD rules
// don't apply — so hostproc is the only substrate that has to enforce it
// itself, same as ensureBridgeNetfilter above.
func ensureForwardAcceptPodCIDR() error {
	return ensureForwardAcceptPodCIDRWith(cluster.PodCIDR, execIptables)
}

// removeForwardAcceptPodCIDR removes the FORWARD ACCEPT rules
// ensureForwardAcceptPodCIDR added for the cluster's pod CIDR, so a host
// rask ran on is left exactly as Start found it — rask owns the host
// netns (see package doc), so anything it adds to it, it must take back.
// Called from Stop alongside removeCNIBridge (see teardown.go): both are
// host-level artifacts a cluster's networking leaves behind.
//
// Best-effort like removeCNIBridge: a rule that's already gone (a
// previous Stop already removed it, or the host has no iptables at all)
// is not an error worth failing Stop over.
func removeForwardAcceptPodCIDR() {
	removeForwardAcceptPodCIDRWith(cluster.PodCIDR, execIptables)
}

// iptablesRunner runs the iptables command with args and returns its
// combined stdout+stderr and error, exactly as exec.Cmd.CombinedOutput
// would — injected so ensureForwardAcceptPodCIDRWith and
// removeForwardAcceptPodCIDRWith are unit-testable without a real
// iptables binary or root privileges.
type iptablesRunner func(args ...string) ([]byte, error)

// execIptables is the production iptablesRunner.
func execIptables(args ...string) ([]byte, error) {
	return exec.Command("iptables", args...).CombinedOutput()
}

// forwardAcceptRuleArgs returns the FORWARD-chain match args (everything
// after the -C/-I/-D verb) for direction dir ("-s" or "-d") and podCIDR,
// shared by the ensure and remove paths so their idea of "the rule" can
// never drift apart.
func forwardAcceptRuleArgs(dir, podCIDR string) []string {
	return []string{"FORWARD", dir, podCIDR, "-j", "ACCEPT"}
}

// ensureForwardAcceptPodCIDRWith is ensureForwardAcceptPodCIDR with the
// pod CIDR and iptables runner injected so the logic is unit-testable.
// For each direction it checks with "iptables -C" first and only inserts
// with "iptables -I ... FORWARD" (at the top of the chain, ahead of
// Docker's DROP policy) when the rule isn't already present, so a
// repeated "rask create" never stacks duplicate rules.
func ensureForwardAcceptPodCIDRWith(podCIDR string, run iptablesRunner) error {
	for _, dir := range []string{"-s", "-d"} {
		args := forwardAcceptRuleArgs(dir, podCIDR)

		exists, err := iptablesRuleExists(run, args)
		if err != nil {
			return forwardAcceptPodCIDRErr(dir, podCIDR, err)
		}

		if exists {
			continue
		}

		if _, err := run(append([]string{"-I"}, args...)...); err != nil {
			return forwardAcceptPodCIDRErr(dir, podCIDR, err)
		}
	}

	return nil
}

// iptablesRuleExists reports whether the FORWARD rule matching args is
// already present, via "iptables -C". Per iptables' documented -C
// behavior it only ever exits 0 (rule exists) or 1 (rule doesn't exist);
// any other outcome — a *exec.ExitError never occurring at all (e.g. the
// iptables binary is missing, so exec.Command itself fails) — is a
// genuine failure the caller must surface, not treated as "not present".
func iptablesRuleExists(run iptablesRunner, args []string) (bool, error) {
	_, err := run(append([]string{"-C"}, args...)...)
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}

	return false, err
}

// forwardAcceptPodCIDRErr builds the error ensureForwardAcceptPodCIDRWith
// returns when it can't confirm or install the FORWARD ACCEPT rule for
// direction dir, in the same voice as ensureBridgeNetfilterFile's error:
// what's wrong, what to do about it, and what silently breaks otherwise.
func forwardAcceptPodCIDRErr(dir, podCIDR string, err error) error {
	return fmt.Errorf("hostproc: checking/inserting the FORWARD ACCEPT rule for pod CIDR %s (%s): %w; ensure iptables is installed and on PATH and that this process can run it as root (e.g. run rask as root, or install iptables), or Docker's default FORWARD DROP policy silently drops all bridged pod-to-pod and pod-to-ClusterIP traffic", podCIDR, dir, err)
}

// removeForwardAcceptPodCIDRWith is removeForwardAcceptPodCIDR with the
// pod CIDR and iptables runner injected so the logic is unit-testable.
func removeForwardAcceptPodCIDRWith(podCIDR string, run iptablesRunner) {
	for _, dir := range []string{"-s", "-d"} {
		args := append([]string{"-D"}, forwardAcceptRuleArgs(dir, podCIDR)...)
		_, _ = run(args...)
	}
}
