#!/usr/bin/env bash
# Builds rask's production arm64 guest kernel Image (uncompressed, no
# modules) from upstream kernel.org source, inside a native-arm64 docker
# container (colima's docker context is an aarch64 VM on Apple Silicon, so
# this is a native build -- no cross toolchain).
#
# Usage: tools/kernel/build.sh
# Env overrides: KERNEL_VERSION (default 6.6.142), JOBS (default nproc in
# the container).
#
# Caches source tarball + ccache + build output under tools/kernel/work/
# (gitignored) so re-runs are fast and idempotent.
set -euo pipefail

KERNEL_VERSION="${KERNEL_VERSION:-6.6.142}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$ROOT/work"
IMAGE_TAG="rask-kernel-builder"
TARBALL="linux-${KERNEL_VERSION}.tar.xz"
TARBALL_URL="https://cdn.kernel.org/pub/linux/kernel/v6.x/${TARBALL}"

mkdir -p "$WORK/src" "$WORK/ccache" "$WORK/out"

echo "==> building builder image ($IMAGE_TAG)"
docker build -q -t "$IMAGE_TAG" -f "$ROOT/Dockerfile.build" "$ROOT" >/dev/null

if [ ! -f "$WORK/src/$TARBALL" ]; then
    echo "==> downloading $TARBALL_URL"
    docker run --rm -v "$WORK/src:/src" "$IMAGE_TAG" \
        curl -fL --retry 8 --retry-all-errors -C - -o "/src/$TARBALL.partial" "$TARBALL_URL"
    mv "$WORK/src/$TARBALL.partial" "$WORK/src/$TARBALL"
else
    echo "==> $TARBALL already cached"
fi

# Source tree and build artifacts live in a named docker volume (the colima
# VM's native disk): virtiofs bind mounts are both slow for kernel builds
# and reject tar's permission/ownership restores.
SRC_VOLUME="rask-kernel-src"
if ! docker run --rm -v "$SRC_VOLUME:/src" "$IMAGE_TAG" test -d "/src/linux-${KERNEL_VERSION}"; then
    echo "==> extracting $TARBALL into volume $SRC_VOLUME"
    docker run --rm -v "$SRC_VOLUME:/src" -v "$WORK/src:/tarball:ro" "$IMAGE_TAG" \
        tar -C /src -xf "/tarball/$TARBALL"
else
    echo "==> volume $SRC_VOLUME already has the source"
fi

echo "==> configuring + building Image (defconfig + rask.config fragment)"
docker run --rm \
    -v "$SRC_VOLUME:/kernel/srcroot" \
    -v "$WORK/ccache:/ccache" \
    -v "$ROOT/rask.config:/kernel/rask.config:ro" \
    -v "$WORK/out:/kernel/out" \
    -e CCACHE_DIR=/ccache \
    -e KBUILD_BUILD_TIMESTAMP="" \
    -e KERNEL_VERSION="$KERNEL_VERSION" \
    "$IMAGE_TAG" bash -c '
        set -euo pipefail
        cd "/kernel/srcroot/linux-${KERNEL_VERSION}"
        JOBS="${JOBS:-$(nproc)}"

        make ARCH=arm64 defconfig
        ./scripts/kconfig/merge_config.sh -m -O . .config /kernel/rask.config
        make ARCH=arm64 olddefconfig

        echo "--- fragment values not honored in final .config (unmet deps, expected empty) ---"
        mismatch=0
        while IFS= read -r line; do
            case "$line" in
                CONFIG_*=*|"# CONFIG_"*" is not set")
                    grep -qxF "$line" .config || { echo "MISMATCH: $line"; mismatch=1; }
                    ;;
            esac
        done < /kernel/rask.config
        if [ "$mismatch" -ne 0 ]; then
            echo "rask.config has values that did not survive olddefconfig (unmet Kconfig deps) -- fix the fragment before building. See tools/kernel/README.md Config rationale for the IPV6=y precedent." >&2
            exit 1
        fi

        echo "--- building Image (no modules) ---"
        make ARCH=arm64 CC="ccache gcc" -j"$JOBS" Image

        cp arch/arm64/boot/Image /kernel/out/Image
        cp .config /kernel/out/config-used
        make ARCH=arm64 kernelversion > /kernel/out/kernelversion
    '

cp "$WORK/out/Image" "$WORK/Image"
cp "$WORK/out/config-used" "$WORK/config-used"

echo "==> done: $WORK/Image"
ls -la "$WORK/Image"
echo "==> ARM64 magic check (bytes at offset 0x38 should read 'ARMd'):"
dd if="$WORK/Image" bs=1 skip=56 count=4 2>/dev/null | cat -v
echo
