#!/usr/bin/env bash
# test/e2e/fjord-seams.sh exercises the 4 fjord-integration seams
# (--component-dir, --apiserver-arg, --preboot-file, --coredns-image)
# against real binaries inside a running colima Linux VM, mirroring
# test/e2e/linux.sh's structure and boundaries (does not start/stop/resize
# colima; cleans up exclusively via "rask delete cluster", never a
# process-name pattern kill). Runs one cluster at a time, sequentially,
# deleting each before starting the next — this VM is shared with other
# long-running clusters.
#
# Scenario (a): --apiserver-arg + --preboot-file together, exercising
#   --authentication-token-webhook-config-file with a dummy kubeconfig
#   pointing at a dead endpoint. Webhook authn is looked up lazily per
#   incoming request, so the apiserver must still boot and become healthy
#   even though the webhook endpoint itself is unreachable — this is
#   exactly how fjord intends to use the flag.
# Scenario (b): --component-dir pointing at binaries copied out of rask's
#   own normal download cache (proves the seam without needing a real
#   EKS-D download).
# Scenario (c): --coredns-image set to the exact same default image ref
#   (plumbing-only proof, no behavior change expected).
#
# Usage: test/e2e/fjord-seams.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

GO="${GO:-go}"

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

BIN="${REPO_ROOT}/rask.e2e.fjord-seams-${GOARCH}"
KUBECTL="/root/.rask/cache/k8s-v1.33.13-${GOARCH}/kubectl"

# Read the default CoreDNS image straight from source rather than
# hardcoding it a second time here, so this script can't silently drift
# from internal/manifests.CoreDNSImage.
DEFAULT_COREDNS_IMAGE="$(grep -oE '"registry\.k8s\.io/coredns/coredns:[^"]+"' internal/manifests/coredns.go | tr -d '"')"
if [[ -z "${DEFAULT_COREDNS_IMAGE}" ]]; then
	echo "could not extract CoreDNSImage from internal/manifests/coredns.go" >&2
	exit 1
fi

echo "==> building rask for linux/${GOARCH}"
GOOS=linux GOARCH="${GOARCH}" "${GO}" build -o "${BIN}" ./cmd/rask

delete_leftover_clusters() {
	echo "==> deleting any leftover e2e-fjord-* clusters (best-effort, by exact name)"
	for name in e2e-fjord-webhook e2e-fjord-componentdir e2e-fjord-coredns e2e-fjord-cache-warm; do
		colima ssh -- sudo "${BIN}" delete cluster --name "${name}" >/dev/null 2>&1 || true
	done
}

cleanup() {
	delete_leftover_clusters
	colima ssh -- sudo rm -f /root/e2e-fjord-webhook-src.yaml
	colima ssh -- sudo rm -rf /root/e2e-fjord-component-dir
	rm -f "${BIN}"
}
trap cleanup EXIT

# Best-effort: leftover clusters from a previous failed run would make
# "create" fail with "already exists". Does not remove the binary just
# built above (unlike the full cleanup(), which also runs on exit).
delete_leftover_clusters

echo ""
echo "=== scenario (a): --apiserver-arg + --preboot-file (authn webhook, dead endpoint) ==="

WEBHOOK_DEST="auth/webhook.yaml"
WEBHOOK_SRC="/root/e2e-fjord-webhook-src.yaml"
WEBHOOK_ABS_DEST="/root/.rask/clusters/e2e-fjord-webhook/data/preboot/${WEBHOOK_DEST}"

colima ssh -- sudo sh -c "cat > ${WEBHOOK_SRC}" <<'EOF'
apiVersion: v1
kind: Config
clusters:
- name: webhook
  cluster:
    server: https://127.0.0.1:9/authenticate
current-context: webhook
contexts:
- context:
    cluster: webhook
    user: webhook-client
  name: webhook
users:
- name: webhook-client
EOF

echo "==> rask create cluster --name e2e-fjord-webhook --preboot-file ${WEBHOOK_SRC}=${WEBHOOK_DEST} --apiserver-arg authentication-token-webhook-config-file=${WEBHOOK_ABS_DEST}"
colima ssh -- sudo "${BIN}" create cluster --name e2e-fjord-webhook --wait node --verbose \
	--preboot-file "${WEBHOOK_SRC}=${WEBHOOK_DEST}" \
	--apiserver-arg "authentication-token-webhook-config-file=${WEBHOOK_ABS_DEST}"

echo "==> verifying the preboot file landed at the documented absolute path"
colima ssh -- sudo sh -c "test -f ${WEBHOOK_ABS_DEST}"

echo "==> verifying node Ready despite the webhook endpoint being unreachable (lazy lookup)"
colima ssh -- sudo sh -c "${KUBECTL} --kubeconfig=/root/.rask/clusters/e2e-fjord-webhook/kubeconfig get nodes"

echo "==> rask delete cluster --name e2e-fjord-webhook"
colima ssh -- sudo "${BIN}" delete cluster --name e2e-fjord-webhook

echo ""
echo "=== scenario (b): --component-dir (binaries copied out of the normal cache) ==="

COMPONENT_DIR="/root/e2e-fjord-component-dir"
CACHE_K8S_DIR="/root/.rask/cache/k8s-v1.33.13-${GOARCH}"

colima ssh -- sudo test -d "${CACHE_K8S_DIR}" || {
	echo "==> warming the default component cache first (component-dir needs a source to copy from)"
	colima ssh -- sudo "${BIN}" create cluster --name e2e-fjord-cache-warm --wait node
	colima ssh -- sudo "${BIN}" delete cluster --name e2e-fjord-cache-warm
}

colima ssh -- sudo sh -c "mkdir -p ${COMPONENT_DIR} && for b in kube-apiserver kube-controller-manager kube-scheduler kubelet kubectl; do cp ${CACHE_K8S_DIR}/\${b} ${COMPONENT_DIR}/\${b} && chmod +x ${COMPONENT_DIR}/\${b}; done"

echo "==> rask create cluster --name e2e-fjord-componentdir --component-dir ${COMPONENT_DIR}"
colima ssh -- sudo "${BIN}" create cluster --name e2e-fjord-componentdir --wait node --verbose --component-dir "${COMPONENT_DIR}"

echo "==> verifying node Ready using the component-dir-sourced binaries"
colima ssh -- sudo sh -c "${KUBECTL} --kubeconfig=/root/.rask/clusters/e2e-fjord-componentdir/kubeconfig get nodes"

echo "==> rask delete cluster --name e2e-fjord-componentdir"
colima ssh -- sudo "${BIN}" delete cluster --name e2e-fjord-componentdir

echo ""
echo "=== scenario (c): --coredns-image (same as the default, plumbing-only proof) ==="

echo "==> rask create cluster --name e2e-fjord-coredns --coredns-image ${DEFAULT_COREDNS_IMAGE}"
colima ssh -- sudo "${BIN}" create cluster --name e2e-fjord-coredns --wait coredns --coredns-image "${DEFAULT_COREDNS_IMAGE}"

echo "==> verifying CoreDNS Ready and using the expected image"
colima ssh -- sudo sh -c "${KUBECTL} --kubeconfig=/root/.rask/clusters/e2e-fjord-coredns/kubeconfig get deploy -n kube-system coredns -o jsonpath='{.status.readyReplicas}'" | grep -q '^[1-9]'
GOT_IMAGE="$(colima ssh -- sudo sh -c "${KUBECTL} --kubeconfig=/root/.rask/clusters/e2e-fjord-coredns/kubeconfig get deploy -n kube-system coredns -o jsonpath='{.spec.template.spec.containers[0].image}'")"
if [[ "${GOT_IMAGE}" != "${DEFAULT_COREDNS_IMAGE}" ]]; then
	echo "CoreDNS image = ${GOT_IMAGE}, want ${DEFAULT_COREDNS_IMAGE}" >&2
	exit 1
fi

echo "==> rask delete cluster --name e2e-fjord-coredns"
colima ssh -- sudo "${BIN}" delete cluster --name e2e-fjord-coredns

trap - EXIT
cleanup

echo ""
echo "==> PASS: all 3 fjord-seam scenarios succeeded"
