#!/usr/bin/env bash
# Fetches the same uncompressed arm64 Linux kernel Image spike S2 already
# validated (puipui-linux v1.0.3, virtio-console/net/vsock built in). If S2's
# copy is present on disk (spikes/s2/work/Image) this just copies it
# read-only from there instead of re-downloading; otherwise it downloads it
# directly. See spikes/s2/fetch.sh and spikes/s2/RESULTS.md for the kernel
# selection rationale, which applies unchanged here.
set -euo pipefail

VERSION="1.0.3"
ARCH="aarch64"
SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$SPIKE_DIR/work"
S2_IMAGE="$SPIKE_DIR/../s2/work/Image"
TARBALL="puipui_linux_v${VERSION}_${ARCH}.tar.gz"
URL="https://github.com/Code-Hex/puipui-linux/releases/download/v${VERSION}/${TARBALL}"

mkdir -p "$WORK_DIR"
cd "$WORK_DIR"

if [[ ! -f Image ]]; then
  if [[ -f "$S2_IMAGE" ]]; then
    cp "$S2_IMAGE" Image
  else
    curl -L -o "$TARBALL" "$URL"
    tar xzf "$TARBALL"
    gunzip -k -f Image.gz
  fi
fi

file Image
echo "kernel ready: $WORK_DIR/Image"
