#!/usr/bin/env bash
# test/e2e/load.sh drives a real "rask load docker-image" / "rask load
# image-archive" cycle against real binaries inside a running colima Linux
# VM (this script does not start, stop or resize colima itself — it must
# already be running), on top of the same create/delete cycle
# test/e2e/linux.sh already validates. It validates this milestone's exit
# criteria:
#
#   - "rask load docker-image" streams an image straight from the local
#     Docker daemon into the cluster's containerd, with no registry access
#   - "rask load image-archive" loads a tar already on disk (the
#     no-Docker-daemon path)
#   - a pod with imagePullPolicy: Never actually starts from a loaded image
#     (proving it came from the load, not a registry pull)
#
# Usage: test/e2e/load.sh [cluster-name]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

GO="${GO:-go}"
CLUSTER_NAME="${1:-e2e-load}"

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

BIN="${REPO_ROOT}/rask.e2e.load-${GOARCH}"
KUBECTL="/root/.rask/cache/k8s-v1.33.13-arm64/kubectl"
KUBECONFIG_FLAG="--kubeconfig=/root/.rask/clusters/${CLUSTER_NAME}/kubeconfig"

# Unique tags per run so a leftover image from a previous failed run can
# never be mistaken for evidence that this run's load actually worked.
RUN_ID="$(date +%s)"
DOCKER_TAG="rask-load-e2e:${RUN_ID}-docker"
ARCHIVE_TAG="rask-load-e2e:${RUN_ID}-archive"
ARCHIVE_PATH="/tmp/rask-load-e2e-${RUN_ID}.tar"

echo "==> building rask for linux/${GOARCH}"
GOOS=linux GOARCH="${GOARCH}" "${GO}" build -o "${BIN}" ./cmd/rask

# cleanup is registered once as an EXIT trap (not called again explicitly
# once the happy path also reaches cluster deletion): every step in it is
# best-effort ("|| true"), so it's safe to let it run unconditionally
# whether the script is exiting after success or failure.
cleanup() {
	echo "==> cleanup: deleting cluster ${CLUSTER_NAME} (best-effort)"
	colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
	rm -f "${BIN}"
	docker rmi "${DOCKER_TAG}" "${ARCHIVE_TAG}" >/dev/null 2>&1 || true
	rm -f "${ARCHIVE_PATH}" || true
	colima ssh -- sudo rm -f "${ARCHIVE_PATH}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Best-effort: a leftover cluster from a previous failed run would make
# "create" fail with "already exists".
colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true

echo "==> preparing test images: retagging busybox as ${DOCKER_TAG} / ${ARCHIVE_TAG}"
docker image inspect busybox:latest >/dev/null 2>&1 || docker pull busybox:latest
docker tag busybox:latest "${DOCKER_TAG}"
docker tag busybox:latest "${ARCHIVE_TAG}"
docker save "${ARCHIVE_TAG}" -o "${ARCHIVE_PATH}"
# rask load image-archive runs inside colima (it needs the cluster's
# containerd socket, only reachable from inside the VM); the archive must
# be readable there too.
cat "${ARCHIVE_PATH}" | colima ssh -- sudo tee "${ARCHIVE_PATH}" >/dev/null

echo "==> rask create cluster --name ${CLUSTER_NAME} --wait coredns"
colima ssh -- sudo "${BIN}" create cluster --name "${CLUSTER_NAME}" --wait coredns

echo "==> rask load docker-image ${DOCKER_TAG} (streams from colima's local dockerd)"
LOAD_START="$(date +%s%N)"
colima ssh -- sudo "${BIN}" load docker-image "${DOCKER_TAG}" --name "${CLUSTER_NAME}"
LOAD_END="$(date +%s%N)"
LOAD_MS=$((("${LOAD_END}" - "${LOAD_START}") / 1000000))
echo "    load docker-image took ${LOAD_MS}ms for $(docker image inspect ${DOCKER_TAG} --format '{{.Size}}') bytes (uncompressed)"

echo "==> running a pod with imagePullPolicy: Never against ${DOCKER_TAG}"
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} delete pod load-smoke-docker --ignore-not-found --wait=true" >/dev/null 2>&1 || true
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} run load-smoke-docker --image=${DOCKER_TAG} --image-pull-policy=Never --restart=Never --command -- sleep 300"

echo "==> waiting for load-smoke-docker to reach Running"
phase=""
for _ in $(seq 1 30); do
	phase="$(colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} get pod load-smoke-docker -o jsonpath='{.status.phase}'" 2>/dev/null || true)"
	if [[ "${phase}" == "Running" ]]; then
		echo "load-smoke-docker is Running"
		break
	fi
	sleep 2
done

if [[ "${phase:-}" != "Running" ]]; then
	echo "load-smoke-docker did not reach Running in time" >&2
	colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} describe pod load-smoke-docker" || true
	exit 1
fi

echo "==> rask load image-archive ${ARCHIVE_PATH} (no Docker daemon involved)"
LOAD2_START="$(date +%s%N)"
colima ssh -- sudo "${BIN}" load image-archive "${ARCHIVE_PATH}" --name "${CLUSTER_NAME}"
LOAD2_END="$(date +%s%N)"
LOAD2_MS=$((("${LOAD2_END}" - "${LOAD2_START}") / 1000000))
echo "    load image-archive took ${LOAD2_MS}ms"

echo "==> running a pod with imagePullPolicy: Never against ${ARCHIVE_TAG}"
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} delete pod load-smoke-archive --ignore-not-found --wait=true" >/dev/null 2>&1 || true
colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} run load-smoke-archive --image=${ARCHIVE_TAG} --image-pull-policy=Never --restart=Never --command -- sleep 300"

echo "==> waiting for load-smoke-archive to reach Running"
phase=""
for _ in $(seq 1 30); do
	phase="$(colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} get pod load-smoke-archive -o jsonpath='{.status.phase}'" 2>/dev/null || true)"
	if [[ "${phase}" == "Running" ]]; then
		echo "load-smoke-archive is Running"
		break
	fi
	sleep 2
done

if [[ "${phase:-}" != "Running" ]]; then
	echo "load-smoke-archive did not reach Running in time" >&2
	colima ssh -- sudo sh -c "${KUBECTL} ${KUBECONFIG_FLAG} describe pod load-smoke-archive" || true
	exit 1
fi

echo "==> PASS: docker-image load (${LOAD_MS}ms) + image-archive load (${LOAD2_MS}ms), both reached Running with imagePullPolicy: Never"
# cleanup (registered above) runs on exit and deletes the cluster.
