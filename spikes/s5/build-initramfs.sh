#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$DIR/work/root"
(cd "$DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o work/root/init ./init)
chmod 0755 "$DIR/work/root/init"
(cd "$DIR/work/root" && find . | cpio -o -H newc 2>/dev/null) > "$DIR/work/initramfs.cpio"
echo "initramfs ready"
