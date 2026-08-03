package guestinit

import "testing"

func TestAMD64ELFRegistration_NoTrailingNUL(t *testing.T) {
	t.Parallel()

	got := AMD64ELFRegistration("/mnt/rosetta/rosetta")

	if len(got) == 0 || got[len(got)-1] == 0 {
		// Found during the M0 s3 spike: a trailing NUL makes the kernel's
		// binfmt_misc/register write fail with EINVAL.
		t.Fatalf("AMD64ELFRegistration ends with a NUL byte: %q", got)
	}
}

func TestAMD64ELFRegistration_Shape(t *testing.T) {
	t.Parallel()

	got := AMD64ELFRegistration("/mnt/rosetta/rosetta")

	const want = ":rosetta:M::\\x7f\\x45\\x4c\\x46\\x02\\x01\\x01\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x02\\x00\\x3e\\x00:\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\x00\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xfe\\xff\\xff\\xff:/mnt/rosetta/rosetta:OF"

	if got != want {
		t.Errorf("AMD64ELFRegistration =\n%s\nwant\n%s", got, want)
	}
}

func TestAMD64ELFMagicMask_MasksOSABIAndETypeLSB(t *testing.T) {
	t.Parallel()

	magic, mask := AMD64ELFMagicMask()

	if len(magic) != len(mask) {
		t.Fatalf("magic/mask length mismatch: %d vs %d", len(magic), len(mask))
	}

	if mask[7] != 0x00 {
		t.Errorf("mask[7] (EI_OSABI) = %#x, want 0x00 (don't care)", mask[7])
	}

	if mask[16] != 0xfe {
		t.Errorf("mask[16] (e_type LSB) = %#x, want 0xfe", mask[16])
	}

	if magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		t.Errorf("magic[0:4] = %v, want ELF magic", magic[0:4])
	}
}
