package components

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Pinned Alpine linux-virt guest kernel version, for rask's macOS vz
// substrate. This is v1's guest kernel: a purpose-built kernel with every
// module rask needs compiled in (=y) is planned as a GitHub Actions
// cross-compile pipeline under tools/kernel/, not a local build
// (colima-class CPU/network constraints rule out building it here).
const (
	AlpineVersion = "v3.21"

	// GuestKernelPkgVersion is the exact linux-virt apk version pinned.
	GuestKernelPkgVersion = "6.12.98-r0"

	// GuestKernelRelease is the kernel release string the package's
	// lib/modules/<release> directory is named after (`uname -r` inside
	// a guest booted from this kernel).
	GuestKernelRelease = "6.12.98-0-virt"

	// guestKernelSHA256 pins the exact linux-virt-<GuestKernelPkgVersion>.apk
	// file downloaded from Alpine's aarch64 main repo, computed against a
	// real download and cross-checked against the mirror at
	// dl-cdn.alpinelinux.org/alpine/<AlpineVersion>/main/aarch64/.
	//
	// This is a plain sha256 of the whole .apk file, not Alpine's own
	// APKINDEX "C:" checksum: that field ("Q1" + base64(sha1(...))) is
	// computed over an apk-tools-internal sub-blob (the control+data
	// tar.gz streams, excluding the leading detached RSA signature
	// stream), not the whole file, and isn't meant as a general-purpose
	// integrity feed the way dl.k8s.io/GitHub release checksum files are.
	// Pinning a plain sha256 of the exact bytes rask downloads is simpler
	// to verify and just as tamper-evident for this purpose.
	guestKernelSHA256 = "50cbf3e95bdce3c65f7890d8055345efd27b877cb6d272af0d278ff12e2b7cb5"
)

func guestKernelURL() string {
	return fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/%s/main/aarch64/linux-virt-%s.apk", AlpineVersion, GuestKernelPkgVersion)
}

// GuestKernel is the resolved, ready-to-boot arm64 kernel and module tree
// for rask's vz substrate guest.
type GuestKernel struct {
	// ImagePath is a raw, uncompressed arm64 Linux kernel Image:
	// vz.NewLinuxBootLoader requires this exact format (confirmed during
	// the M0 s2 spike — no decompression happens on the vz side),
	// but Alpine ships vmlinuz-virt as an EFI-ZBOOT PE wrapping a
	// gzip-compressed payload, so EnsureGuestKernel unwraps it once at
	// fetch time (see extractZimgPayload) rather than on every boot.
	ImagePath string

	// ModulesDir is lib/modules/<Release>, containing modules.dep and
	// every .ko.gz module rask-init may load (see internal/guestinit).
	ModulesDir string

	Release string
}

// EnsureGuestKernel downloads (if not already cached), verifies and unwraps
// Alpine's linux-virt aarch64 kernel package, returning the resolved,
// directly-bootable kernel and its module tree. Idempotent: a second call
// with the same cache dir does no network I/O.
func (c *Cache) EnsureGuestKernel(ctx context.Context) (*GuestKernel, error) {
	subdir := fmt.Sprintf("guest-kernel-%s", GuestKernelPkgVersion)
	dir := filepath.Join(c.dir, subdir)
	imagePath := filepath.Join(dir, "Image")
	modulesDir := filepath.Join(dir, "lib", "modules", GuestKernelRelease)

	if _, err := os.Stat(imagePath); err == nil {
		return &GuestKernel{ImagePath: imagePath, ModulesDir: modulesDir, Release: GuestKernelRelease}, nil
	}

	data, err := c.fetch(ctx, guestKernelURL())
	if err != nil {
		return nil, fmt.Errorf("components: downloading guest kernel: %w", err)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != guestKernelSHA256 {
		return nil, fmt.Errorf("components: guest kernel sha256 mismatch: got %s, want %s", got, guestKernelSHA256)
	}

	tmpDir := dir + ".tmp"
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, fmt.Errorf("components: clearing stale %s: %w", tmpDir, err)
	}

	// Alpine .apk files are three concatenated gzip streams (detached
	// signature, control/.PKGINFO, data) — compress/gzip.Reader defaults
	// to Multistream(true), so the same extractTarGz used for containerd
	// and cni-plugins's tar.gz releases reads straight through all three
	// as one logical archive, no apk-specific handling needed.
	if err := extractTarGz(data, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)

		return nil, fmt.Errorf("components: extracting guest kernel package: %w", err)
	}

	rawImage, err := extractZimgPayload(filepath.Join(tmpDir, "boot", "vmlinuz-virt"))
	if err != nil {
		_ = os.RemoveAll(tmpDir)

		return nil, fmt.Errorf("components: unwrapping guest kernel: %w", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "Image"), rawImage, 0o644); err != nil {
		_ = os.RemoveAll(tmpDir)

		return nil, fmt.Errorf("components: writing guest kernel Image: %w", err)
	}

	if err := os.Rename(tmpDir, dir); err != nil {
		return nil, fmt.Errorf("components: finalizing guest kernel cache dir: %w", err)
	}

	return &GuestKernel{ImagePath: imagePath, ModulesDir: modulesDir, Release: GuestKernelRelease}, nil
}

// zimgHeaderSize is the fixed-size header extractZimgPayload reads before
// the gzip-compressed kernel payload begins.
const zimgHeaderSize = 0x18

// extractZimgPayload extracts the raw, gzip-compressed arm64 kernel Image
// payload from an Alpine EFI-ZBOOT vmlinuz-virt PE file and decompresses
// it.
//
// Layout (confirmed by reading the actual bytes of a downloaded
// vmlinuz-virt, per the M0 s3 spike's production recipe):
//
//	offset 0x00: "MZ\x00\x00"      PE stub magic
//	offset 0x04: "zimg"            EFI-ZBOOT magic
//	offset 0x08: uint32 LE         payload offset
//	offset 0x0c: uint32 LE         payload size
//	offset 0x10: 8 reserved bytes
//	offset 0x18: "gzip"            compression type tag
//	offset 0x1c: payload starts elsewhere in the file, at the offset above
//
// vz.NewLinuxBootLoader cannot boot this PE/ZBOOT wrapper directly (no
// decompression happens on the vz side), so this must run once at fetch
// time, not on every VM boot.
func extractZimgPayload(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if len(data) < zimgHeaderSize || string(data[4:8]) != "zimg" {
		return nil, fmt.Errorf("%s: not an EFI-ZBOOT image (missing \"zimg\" magic at offset 4)", path)
	}

	off := binary.LittleEndian.Uint32(data[8:12])
	size := binary.LittleEndian.Uint32(data[12:16])

	end := uint64(off) + uint64(size)
	if end > uint64(len(data)) {
		return nil, fmt.Errorf("%s: zimg payload [%d:%d] exceeds file length %d", path, off, end, len(data))
	}

	payload := data[off:end]

	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: opening zimg payload as gzip: %w", path, err)
	}
	defer func() { _ = gz.Close() }()

	raw, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("%s: decompressing zimg payload: %w", path, err)
	}

	return raw, nil
}
