// Package binfmt builds a Linux binfmt_misc registration line for routing
// amd64 ELF binaries through an interpreter (Rosetta, in rask's case). It
// has no OS-specific dependencies -- writing the registration to
// /proc/sys/fs/binfmt_misc/register is the caller's job -- so it can be
// built and tested on any host, unlike the rest of rask-init.
package binfmt

import "fmt"

// eiNIdent is ELF's e_ident size (elf.h).
const eiNIdent = 16

// AMD64ELFMagicMask builds the binfmt_misc magic/mask byte pair matching an
// ELF64 little-endian x86-64 header (e_ident + e_type + e_machine, offsets
// 0-19 per the ELF ABI), with two fields relaxed:
//
//   - EI_OSABI (offset 7) is masked out: some publishers set it (e.g. to
//     ELFOSABI_LINUX) and some leave it 0 (ELFOSABI_NONE); both are valid
//     Linux binaries.
//   - the low bit of e_type (offset 16) is masked out, so the entry matches
//     both ET_EXEC (2, static/non-PIE) and ET_DYN (3, PIE) -- almost all
//     modern distro binaries are ET_DYN.
func AMD64ELFMagicMask() (magic, mask []byte) {
	magic = make([]byte, 0, eiNIdent+4)
	magic = append(magic, 0x7f, 'E', 'L', 'F') // EI_MAG0..3
	magic = append(magic, 2)                   // EI_CLASS = ELFCLASS64
	magic = append(magic, 1)                   // EI_DATA = ELFDATA2LSB
	magic = append(magic, 1)                   // EI_VERSION = EV_CURRENT
	magic = append(magic, 0)                   // EI_OSABI (masked below)
	magic = append(magic, make([]byte, eiNIdent-len(magic))...)
	magic = append(magic, 0x02, 0x00) // e_type = ET_EXEC (LSB masked below)
	magic = append(magic, 0x3e, 0x00) // e_machine = EM_X86_64 (62), little-endian

	mask = make([]byte, len(magic))
	for i := range mask {
		mask[i] = 0xff
	}
	mask[7] = 0x00  // EI_OSABI: don't care
	mask[16] = 0xfe // e_type: match ET_EXEC or ET_DYN

	return magic, mask
}

// AMD64ELFRegistration renders the full binfmt_misc register-file line for
// routing amd64 ELF exec()s through interpreterPath.
//
// Flags "OF":
//   - F (fix-binary): resolves and opens interpreterPath once, at
//     registration time, rather than by path on every exec. Required
//     because containers run in their own mount namespace via runc, where
//     the host's Rosetta share is not visible -- without F the kernel
//     couldn't resolve the interpreter path after the container's
//     pivot_root.
//   - O (open-binary): passes the target amd64 binary to the interpreter as
//     an already-open file descriptor instead of a path, for the same
//     reason in the other direction -- the target binary lives inside the
//     container's mount namespace/rootfs, which Rosetta cannot otherwise
//     resolve a path into.
func AMD64ELFRegistration(interpreterPath string) string {
	magic, mask := AMD64ELFMagicMask()
	// No trailing terminator: the kernel accepts the bare line, and a
	// trailing NUL makes the write fail with EINVAL (verified empirically
	// on Linux 6.6 and 6.8).
	return fmt.Sprintf(":rosetta:M::%s:%s:%s:OF",
		escape(magic), escape(mask), interpreterPath)
}

// escape renders raw bytes as \xHH escapes, the format binfmt_misc's
// register file expects for the magic/mask fields.
func escape(b []byte) string {
	out := make([]byte, 0, len(b)*4)
	for _, c := range b {
		out = append(out, []byte(fmt.Sprintf("\\x%02x", c))...)
	}
	return string(out)
}
