#!/usr/bin/env bash
# Builds a newc-format cpio initramfs carrying rask-init as /init plus
# containerd/runc/ctr and their config. Unlike S2 (init-only, ~1.7MB), this
# is a deliberately "big initramfs" (~90MB): a writable ext4 disk image
# would need mkfs.ext4, which macOS doesn't ship, and the spike brief
# accepts a big tmpfs-root initramfs as a pragmatic alternative. cpio's
# unpack target *is* Linux's initial rootfs (a real writable tmpfs-like
# fs, not a read-only staging area), so nothing extra is needed to make
# containerd's /var/lib/containerd and /run/containerd writable at runtime.
set -euo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$SPIKE_DIR/work"
ROOT_DIR="$WORK_DIR/initramfs-root"

(cd "$SPIKE_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$WORK_DIR/rask-init" ./init)

rm -rf "$ROOT_DIR"
mkdir -p "$ROOT_DIR/bin" "$ROOT_DIR/etc/containerd"

cp "$WORK_DIR/rask-init" "$ROOT_DIR/init"
chmod 0755 "$ROOT_DIR/init"

for f in containerd containerd-shim-runc-v2 ctr runc; do
  cp "$WORK_DIR/bin/$f" "$ROOT_DIR/bin/$f"
  chmod 0755 "$ROOT_DIR/bin/$f"
done

cp "$SPIKE_DIR/containerd-config.toml" "$ROOT_DIR/etc/containerd/config.toml"

# Kernel modules the Alpine virt kernel doesn't build in (loaded by
# init/modules.go in dependency order).
MODSRC="$WORK_DIR/altkernel/lib/modules/6.6.142-0-virt/kernel"
mkdir -p "$ROOT_DIR/lib/modules"
for m in drivers/char/hw_random/rng-core.ko.gz drivers/char/hw_random/virtio-rng.ko.gz \
         net/packet/af_packet.ko.gz net/core/failover.ko.gz drivers/net/net_failover.ko.gz \
         drivers/net/virtio_net.ko.gz fs/fuse/fuse.ko.gz \
         fs/fuse/virtiofs.ko.gz fs/binfmt_misc.ko.gz \
         fs/overlayfs/overlay.ko.gz; do
  cp "$MODSRC/$m" "$ROOT_DIR/lib/modules/$(basename "$m")"
done

# CA bundle for registry TLS (containerd/ctr read the standard Linux path).
mkdir -p "$ROOT_DIR/etc/ssl/certs"
cp /etc/ssl/cert.pem "$ROOT_DIR/etc/ssl/certs/ca-certificates.crt"

( cd "$ROOT_DIR" && find . | cpio -o -H newc 2>/dev/null ) > "$WORK_DIR/initramfs.cpio"

echo "initramfs ready: $WORK_DIR/initramfs.cpio ($(du -h "$WORK_DIR/initramfs.cpio" | cut -f1))"
