#!/usr/bin/env bash
# Fetches an uncompressed arm64 Linux kernel Image suitable for
# vz.NewLinuxBootLoader (which requires an uncompressed Image on arm64).
#
# Kernel choice: puipui-linux (github.com/Code-Hex/puipui-linux), the
# reference kernel that the Code-Hex/vz binding's own CI test suite boots
# for integration tests. It ships an uncompressed arm64 Image with
# virtio-console, virtio-net, and virtio-vsock already enabled, which is
# exactly the device surface vz's VZLinuxBootLoader exercises. This was
# preferred over the originally considered options because it is small
# (~5MB tarball), known-good against this exact vz version (v3.7.1), and
# needs no further kernel-config verification.
#
# Kata Containers' guest kernel (kata-static-*.tar.xz) was also considered:
# it is purpose-built for fast microVM boot, but its tarball bundles a full
# Kata release (agent, rootfs, QEMU/CH binaries, ~100s of MB) with the
# kernel nested several directories deep, and its default virtio device
# set is tuned for Kata's own agent/vsock protocol rather than a bare
# vz.FileHandleSerialPortAttachment console. Firecracker CI kernels target
# Firecracker's own minimal MMIO device model, not vz's virtio-console.
# Alpine's vmlinuz-virt is gzip-compressed (vz requires uncompressed) and
# frequently ships virtio as loadable modules rather than built in.
set -euo pipefail

VERSION="1.0.3"
ARCH="aarch64"
WORK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/work"
TARBALL="puipui_linux_v${VERSION}_${ARCH}.tar.gz"
URL="https://github.com/Code-Hex/puipui-linux/releases/download/v${VERSION}/${TARBALL}"

mkdir -p "$WORK_DIR"
cd "$WORK_DIR"

if [[ ! -f Image ]]; then
  curl -L -o "$TARBALL" "$URL"
  tar xzf "$TARBALL"
  gunzip -k -f Image.gz
fi

file Image
echo "kernel ready: $WORK_DIR/Image"
