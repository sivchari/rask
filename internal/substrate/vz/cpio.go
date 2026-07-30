//go:build darwin

// cpio.go implements just enough of the "newc" cpio archive format
// (https://www.kernel.org/doc/html/latest/driverapi/early-userspace/buffer-format.html)
// to build a Linux kernel-bootable initramfs: no compress/archive/tar
// package in the standard library speaks it (tar is a different format
// entirely), and the kernel's own initramfs unpacker
// (init/initramfs.c:do_header, do_name) only accepts newc, not the older
// cpio variants.
package vz

import (
	"bytes"
	"fmt"
	"strings"
)

// cpioTrailerName is the sentinel entry name cpio archives (and the
// kernel's initramfs unpacker) use to mark the end of one archive. Two or
// more archives can be concatenated as one initrd (plan-m0-spikes.md's
// per-cluster config transport decision) precisely because the kernel's
// unpacker keeps scanning for another header immediately after seeing this
// entry, rather than stopping at EOF.
const cpioTrailerName = "TRAILER!!!"

// cpioBlockSize is the block size cpioWriter pads the final archive to,
// matching common initramfs-building tools (e.g. gen_init_cpio,
// dracut) — not required by the kernel's unpacker (it tolerates NUL
// padding at any alignment) but standard practice.
const cpioBlockSize = 512

// newc file-mode type bits (see the newc format doc): cpio's c_mode field
// packs a POSIX file type into its high bits, which os.FileMode does not
// use the same encoding for.
const (
	cpioModeDir     = 0o040000
	cpioModeFile    = 0o100000
	cpioModeSymlink = 0o120000
)

// cpioWriter builds a newc-format cpio archive in memory.
type cpioWriter struct {
	buf       bytes.Buffer
	ino       uint32
	dirsSeen  map[string]bool
	filesSeen map[string]bool
}

func newCpioWriter() *cpioWriter {
	return &cpioWriter{dirsSeen: map[string]bool{}, filesSeen: map[string]bool{}}
}

// WriteFile adds a regular file entry at name (a path relative to the
// archive root, no leading "/"), creating any not-yet-seen parent
// directory entries first — the kernel's initramfs unpacker does not
// implicitly create missing parent directories for a file entry.
//
// Writing the same name a second time is a no-op (first write wins),
// rather than an error or a duplicate entry: multiple independently-built
// bundles rask ships (e.g. the iptables and e2fsprogs userland bundles)
// legitimately share some files verbatim (both pin the same musl package),
// and callers merging them shouldn't need to track that overlap themselves.
func (w *cpioWriter) WriteFile(name string, mode uint32, data []byte) error {
	if w.filesSeen[name] {
		return nil
	}

	if err := w.ensureDirChain(parentDir(name)); err != nil {
		return err
	}

	if err := w.writeEntry(name, cpioModeFile|mode, uint32(len(data)), data); err != nil {
		return err
	}

	w.filesSeen[name] = true

	return nil
}

// WriteDir adds a directory entry at name, if not already written (directly
// or as an ancestor of a prior WriteFile/WriteDir/WriteSymlink call).
func (w *cpioWriter) WriteDir(name string, mode uint32) error {
	name = strings.TrimSuffix(name, "/")
	if w.dirsSeen[name] {
		return nil
	}

	if err := w.ensureDirChain(parentDir(name)); err != nil {
		return err
	}

	if err := w.writeEntry(name, cpioModeDir|mode, 0, nil); err != nil {
		return err
	}

	w.dirsSeen[name] = true

	return nil
}

// WriteSymlink adds a symlink entry at name pointing at target. cpio
// encodes the link target as the entry's "file data". Like WriteFile,
// writing the same name a second time is a no-op.
func (w *cpioWriter) WriteSymlink(name, target string) error {
	if w.filesSeen[name] {
		return nil
	}

	if err := w.ensureDirChain(parentDir(name)); err != nil {
		return err
	}

	if err := w.writeEntry(name, cpioModeSymlink|0o777, uint32(len(target)), []byte(target)); err != nil {
		return err
	}

	w.filesSeen[name] = true

	return nil
}

// ensureDirChain writes a directory entry (mode 0o755) for dir and every
// not-yet-seen ancestor of dir, shallowest first. A no-op for "" or "."
// (the archive root, which is never written as its own entry) or a dir
// that's already been written.
func (w *cpioWriter) ensureDirChain(dir string) error {
	if dir == "" || dir == "." || w.dirsSeen[dir] {
		return nil
	}

	if err := w.ensureDirChain(parentDir(dir)); err != nil {
		return err
	}

	if err := w.writeEntry(dir, cpioModeDir|0o755, 0, nil); err != nil {
		return err
	}

	w.dirsSeen[dir] = true

	return nil
}

// parentDir returns name's parent directory in cpio-archive path terms
// (forward-slash separated, no leading "/"), or "" if name has none.
func parentDir(name string) string {
	idx := strings.LastIndexByte(name, '/')
	if idx < 0 {
		return ""
	}

	return name[:idx]
}

func (w *cpioWriter) writeEntry(name string, mode, size uint32, data []byte) error {
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("vz: cpio entry %q must be a relative path", name)
	}

	w.ino++

	// namesize includes the trailing NUL the newc format requires after
	// the pathname.
	nameBytes := append([]byte(name), 0)

	header := fmt.Sprintf("070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		w.ino,          // c_ino
		mode,           // c_mode
		0,              // c_uid
		0,              // c_gid
		1,              // c_nlink
		0,              // c_mtime
		size,           // c_filesize
		0,              // c_devmajor
		0,              // c_devminor
		0,              // c_rdevmajor
		0,              // c_rdevminor
		len(nameBytes), // c_namesize
		0,              // c_check
	)

	w.buf.WriteString(header)
	w.buf.Write(nameBytes)
	padTo4(&w.buf)

	if len(data) > 0 {
		w.buf.Write(data)
		padTo4(&w.buf)
	}

	return nil
}

// padTo4 writes NUL bytes until buf's length is a multiple of 4, which the
// newc format requires after both the header+name and the file data.
func padTo4(buf *bytes.Buffer) {
	for buf.Len()%4 != 0 {
		buf.WriteByte(0)
	}
}

// Finish writes the archive's trailer entry and pads the result to
// cpioBlockSize, returning the complete archive.
func (w *cpioWriter) Finish() ([]byte, error) {
	if err := w.writeEntry(cpioTrailerName, 0, 0, nil); err != nil {
		return nil, err
	}

	for w.buf.Len()%cpioBlockSize != 0 {
		w.buf.WriteByte(0)
	}

	return w.buf.Bytes(), nil
}
