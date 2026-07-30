package binfmt

import "testing"

// TestAMD64ELFMagicMask pins down the byte-for-byte ELF header pattern used
// to register Rosetta as the amd64 binfmt_misc interpreter. A single offset
// mistake here silently breaks binary detection at exec() time, deep inside
// a guest VM where the only feedback is "container failed to start" -- so
// this is checked against the ELF ABI field-by-field rather than trusted by
// inspection.
func TestAMD64ELFMagicMask(t *testing.T) {
	magic, mask := AMD64ELFMagicMask()

	wantMagic := []byte{
		0x7f, 'E', 'L', 'F', // EI_MAG0..3
		2,                   // EI_CLASS = ELFCLASS64
		1,                   // EI_DATA = ELFDATA2LSB
		1,                   // EI_VERSION = EV_CURRENT
		0,                   // EI_OSABI
		0,                   // EI_ABIVERSION
		0, 0, 0, 0, 0, 0, 0, // EI_PAD (7 bytes, offsets 9-15)
		0x02, 0x00, // e_type = ET_EXEC, little-endian
		0x3e, 0x00, // e_machine = EM_X86_64 (62), little-endian
	}
	if len(magic) != 20 {
		t.Fatalf("len(magic) = %d, want 20 (e_ident[16] + e_type[2] + e_machine[2])", len(magic))
	}
	for i := range wantMagic {
		if magic[i] != wantMagic[i] {
			t.Errorf("magic[%d] = %#02x, want %#02x", i, magic[i], wantMagic[i])
		}
	}

	if len(mask) != len(magic) {
		t.Fatalf("len(mask) = %d, want %d (same as magic)", len(mask), len(magic))
	}
	for i, b := range mask {
		want := byte(0xff)
		switch i {
		case 7: // EI_OSABI: don't care
			want = 0x00
		case 16: // e_type low byte: match ET_EXEC(2) or ET_DYN(3)
			want = 0xfe
		}
		if b != want {
			t.Errorf("mask[%d] = %#02x, want %#02x", i, b, want)
		}
	}

	// The masked e_type byte must accept both a static (ET_EXEC=2) and a
	// PIE (ET_DYN=3) executable, since real amd64 binaries are a mix.
	for _, eType := range []byte{2, 3} {
		candidate := append([]byte(nil), magic...)
		candidate[16] = eType
		if !elfHeaderMatches(candidate, magic, mask) {
			t.Errorf("e_type=%d should match magic/mask, did not", eType)
		}
	}
	// A non-x86-64 machine (e.g. EM_AARCH64=183) must not match.
	candidate := append([]byte(nil), magic...)
	candidate[18], candidate[19] = 183, 0
	if elfHeaderMatches(candidate, magic, mask) {
		t.Error("EM_AARCH64 header incorrectly matched the x86-64 magic/mask")
	}
}

// elfHeaderMatches replicates the kernel's binfmt_misc matching rule:
// (header[i] & mask[i]) == (magic[i] & mask[i]) for every byte.
func elfHeaderMatches(header, magic, mask []byte) bool {
	for i := range magic {
		if header[i]&mask[i] != magic[i]&mask[i] {
			return false
		}
	}
	return true
}

func TestAMD64ELFRegistration(t *testing.T) {
	const interp = "/mnt/rosetta/rosetta"
	reg := AMD64ELFRegistration(interp)

	if got, want := reg[:len(":rosetta:M::")], ":rosetta:M::"; got != want {
		t.Errorf("registration prefix = %q, want %q", got, want)
	}
	wantSuffix := ":" + interp + ":OF"
	if len(reg) < len(wantSuffix) || reg[len(reg)-len(wantSuffix):] != wantSuffix {
		t.Errorf("registration suffix = %q, want it to end with %q", reg, wantSuffix)
	}
}
