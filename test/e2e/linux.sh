#!/usr/bin/env bash
# test/e2e/linux.sh drives a real "rask create cluster" / "rask delete
# cluster" cycle against real binaries inside a running colima Linux VM
# (this script does not start, stop or resize colima itself — it must
# already be running). It validates the full path this milestone's exit
# criteria depend on:
#
#   - node Ready
#   - CoreDNS Ready (--wait coredns)
#   - a smoke pod reaches Running
#   - delete cleans up fully: no leftover cluster state directory, no
#     leftover rask-launched processes
#
# Usage: test/e2e/linux.sh [cluster-name]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

GO="${GO:-go}"
CLUSTER_NAME="${1:-e2e-smoke}"

if ! command -v colima >/dev/null 2>&1; then
	echo "colima not found on PATH" >&2
	exit 1
fi

if ! colima status >/dev/null 2>&1; then
	echo "colima is not running; this script does not start it (see boundaries)" >&2
	exit 1
fi

VM_ARCH="$(colima ssh -- uname -m)"
case "${VM_ARCH}" in
arm64 | aarch64) GOARCH=arm64 ;;
x86_64 | amd64) GOARCH=amd64 ;;
*)
	echo "unrecognized VM architecture: ${VM_ARCH}" >&2
	exit 1
	;;
esac

BIN="${REPO_ROOT}/rask.e2e.linux-${GOARCH}"
KUBECTL="/root/.rask/cache/k8s-v1.33.13-arm64/kubectl"

echo "==> building rask for linux/${GOARCH}"
GOOS=linux GOARCH="${GOARCH}" "${GO}" build -o "${BIN}" ./cmd/rask

cleanup() {
	echo "==> cleanup: deleting cluster ${CLUSTER_NAME} (best-effort)"
	colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" || true
	rm -f "${BIN}"
}
trap cleanup EXIT

# Best-effort: a leftover cluster from a previous failed run would make
# "create" fail with "already exists".
colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true

echo "==> rask create cluster --name ${CLUSTER_NAME} --verbose --wait coredns"
colima ssh -- sudo "${BIN}" create cluster --name "${CLUSTER_NAME}" --verbose --wait coredns

KUBECONFIG_FLAG="--kubeconfig=/root/.rask/clusters/${CLUSTER_NAME}/kubeconfig"

echo "==> verifying node Ready"
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} get nodes"

echo "==> verifying CoreDNS Ready"
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} get deploy -n kube-system coredns -o jsonpath='{.status.readyReplicas}'" | grep -q '^[1-9]'

echo "==> running smoke pod"
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} delete pod smoke --ignore-not-found --wait=true" >/dev/null 2>&1 || true
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} run smoke --image=registry.k8s.io/pause:3.10 --restart=Never"

echo "==> waiting for smoke pod to reach Running"
for _ in $(seq 1 30); do
	phase="$(colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} get pod smoke -o jsonpath='{.status.phase}'" 2>/dev/null || true)"
	if [[ "${phase}" == "Running" ]]; then
		echo "smoke pod is Running"
		break
	fi
	sleep 2
done

if [[ "${phase:-}" != "Running" ]]; then
	echo "smoke pod did not reach Running in time" >&2
	exit 1
fi

echo "==> rask delete cluster --name ${CLUSTER_NAME}"
colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}"

echo "==> verifying cluster state directory is gone"
if colima ssh -- sudo sh -c "test -d /root/.rask/clusters/${CLUSTER_NAME}" 2>/dev/null; then
	echo "cluster state directory still present after delete" >&2
	exit 1
fi

echo "==> verifying no rask-launched processes remain"
if colima ssh -- sudo sh -c 'ps aux | grep "/root/.rask/cache" | grep -v grep' >/dev/null 2>&1; then
	echo "rask-launched processes still running after delete" >&2
	exit 1
fi

trap - EXIT
rm -f "${BIN}"

echo "==> PASS: full create/CoreDNS/smoke-pod/delete cycle succeeded"
