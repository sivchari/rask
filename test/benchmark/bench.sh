#!/usr/bin/env bash
# test/benchmark/bench.sh measures N create->Ready cycles of
# `rask create cluster` against real binaries inside a running colima Linux
# VM, reporting p50/p95 wall-clock latency (the "create" invocation itself:
# process start to node Ready, or to CoreDNS Ready with --wait=coredns).
# Does not start, stop or resize colima.
#
# Usage: RUNS=10 WAIT=node test/benchmark/bench.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

GO="${GO:-go}"
RUNS="${RUNS:-10}"
CLUSTER_NAME="${CLUSTER_NAME:-bench}"
WAIT="${WAIT:-node}" # "node" or "coredns"

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

BIN="${REPO_ROOT}/rask.bench.linux-${GOARCH}"
RAW_FILE="${REPO_ROOT}/test/benchmark/.raw-durations-${WAIT}.txt"

echo "==> building rask for linux/${GOARCH}"
GOOS=linux GOARCH="${GOARCH}" "${GO}" build -o "${BIN}" ./cmd/rask

cleanup() {
	colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
	rm -f "${BIN}"
}
trap cleanup EXIT

colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true

echo "==> warming the component cache (first create is not measured)"
colima ssh -- sudo "${BIN}" create cluster --name "${CLUSTER_NAME}" --wait "${WAIT}" >/dev/null
colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null

: >"${RAW_FILE}"

for i in $(seq 1 "${RUNS}"); do
	start=$(date +%s.%N)
	colima ssh -- sudo "${BIN}" create cluster --name "${CLUSTER_NAME}" --wait "${WAIT}" >/dev/null
	end=$(date +%s.%N)
	d=$(python3 -c "print(f'{${end} - ${start}:.3f}')")
	echo "run ${i}/${RUNS}: ${d}s"
	echo "${d}" >>"${RAW_FILE}"
	colima ssh -- sudo "${BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null
done

echo "==> summary (--wait=${WAIT})"
python3 - "${RAW_FILE}" <<'EOF'
import sys, statistics

with open(sys.argv[1]) as f:
    samples = sorted(float(line) for line in f if line.strip())

def percentile(data, p):
    idx = max(0, min(len(data) - 1, int(round(p / 100 * (len(data) - 1)))))
    return data[idx]

print(f"n={len(samples)} p50={percentile(samples,50):.3f}s p95={percentile(samples,95):.3f}s "
      f"min={min(samples):.3f}s max={max(samples):.3f}s mean={statistics.mean(samples):.3f}s")
EOF
