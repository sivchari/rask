#!/usr/bin/env bash
# Builds a minimal newc-format cpio initramfs whose only content is
# rask-init installed as /init. No busybox/shell is included: rask-init
# is PID 1 and does everything itself (mount, marker print, zombie reap).
set -euo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$SPIKE_DIR/work"
ROOT_DIR="$WORK_DIR/initramfs-root"

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$WORK_DIR/rask-init" "$SPIKE_DIR/init"

rm -rf "$ROOT_DIR"
mkdir -p "$ROOT_DIR"
cp "$WORK_DIR/rask-init" "$ROOT_DIR/init"
chmod 0755 "$ROOT_DIR/init"

( cd "$ROOT_DIR" && find . | cpio -o -H newc 2>/dev/null ) > "$WORK_DIR/initramfs.cpio"

echo "initramfs ready: $WORK_DIR/initramfs.cpio ($(du -h "$WORK_DIR/initramfs.cpio" | cut -f1))"
