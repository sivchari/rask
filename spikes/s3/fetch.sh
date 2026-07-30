#!/usr/bin/env bash
# Fetches everything the S3 guest needs beyond rask-init itself:
#   - the puipui-linux kernel S2 already validated against this vz version
#     (reused read-only from spikes/s2/work/Image; downloaded fresh only if
#     that spike's artifact isn't present)
#   - static linux/arm64 containerd + runc + ctr binaries (they run
#     natively on the arm64 guest kernel; binfmt_misc/Rosetta is what lets
#     the *containers* they launch be amd64)
set -euo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$SPIKE_DIR/work"
mkdir -p "$WORK_DIR/bin"
cd "$WORK_DIR"

if [[ ! -f Image ]]; then
  S2_IMAGE="$SPIKE_DIR/../s2/work/Image"
  if [[ -f "$S2_IMAGE" ]]; then
    cp "$S2_IMAGE" Image
    echo "kernel: reused $S2_IMAGE"
  else
    VERSION="1.0.3"
    curl -L -o puipui.tar.gz "https://github.com/Code-Hex/puipui-linux/releases/download/v${VERSION}/puipui_linux_v${VERSION}_aarch64.tar.gz"
    tar xzf puipui.tar.gz
    gunzip -k -f Image.gz
    echo "kernel: downloaded v${VERSION}"
  fi
fi
file Image

CONTAINERD_VERSION="2.3.3"
RUNC_VERSION="1.5.1"

if [[ ! -f bin/containerd ]]; then
  curl -fL -o containerd-static.tar.gz \
    "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-static-${CONTAINERD_VERSION}-linux-arm64.tar.gz"
  tar xzf containerd-static.tar.gz
  rm -f bin/containerd-stress # unused, drop to keep the initramfs smaller
fi

if [[ ! -f bin/runc ]]; then
  curl -fL -o bin/runc "https://github.com/opencontainers/runc/releases/download/v${RUNC_VERSION}/runc.arm64"
  chmod +x bin/runc
fi

for f in bin/containerd bin/containerd-shim-runc-v2 bin/ctr bin/runc; do
  file "$f"
done
echo "guest binaries ready: $WORK_DIR/bin"
