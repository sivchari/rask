//go:build darwin

package vz

import "testing"

// realVMStatSample is real `vm_stat` output captured on the development
// host (page size 16384 bytes, not the historical 4096 default — parsing
// must read this from the header, not assume it).
const realVMStatSample = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                     8900.
Pages active:                                1288000.
Pages inactive:                              1283774.
Pages speculative:                              3011.
Pages throttled:                                   0.
Pages wired down:                             346291.
Pages purgeable:                               30306.
"Translation faults":                     4073131216.
Pages copy-on-write:                       395016109.
Pages zero filled:                        2232782515.
Pages reactivated:                         299308507.
Pages purged:                               50660427.
File-backed pages:                            436933.
Anonymous pages:                             2137852.
Pages stored in compressor:                   330283.
Pages occupied by compressor:                 159970.
Decompressions:                            235591783.
Compressions:                              268538847.
Pageins:                                   162267620.
Pageouts:                                     412284.
Swapins:                                     2907678.
Swapouts:                                    4375155.
`

func TestParseVMStatFreeMiB_RealSample(t *testing.T) {
	t.Parallel()

	got, err := parseVMStatFreeMiB([]byte(realVMStatSample))
	if err != nil {
		t.Fatalf("parseVMStatFreeMiB: %v", err)
	}

	// (8900 free + 1283774 inactive) pages * 16384 bytes / (1024*1024),
	// truncated by integer division.
	want := uint64(20198)

	if got != want {
		t.Errorf("parseVMStatFreeMiB = %d MiB, want %d MiB", got, want)
	}
}

func TestParseVMStatFreeMiB_DefaultsPageSizeWhenHeaderMissing(t *testing.T) {
	t.Parallel()

	// No "(page size of ...)" header at all: falls back to 4096, the
	// historical vm_stat default before Apple Silicon's 16KiB pages.
	sample := "Mach Virtual Memory Statistics:\nPages free:                    1000.\nPages inactive:                 500.\n"

	got, err := parseVMStatFreeMiB([]byte(sample))
	if err != nil {
		t.Fatalf("parseVMStatFreeMiB: %v", err)
	}

	want := uint64(1500) * 4096 / (1024 * 1024)

	if got != want {
		t.Errorf("parseVMStatFreeMiB = %d MiB, want %d MiB", got, want)
	}
}

func TestCheckFreeMemory_RejectsWhenBelowThreshold(t *testing.T) {
	t.Parallel()

	err := checkFreeMemoryFromStats(100, 8192) // 100MiB free, wants 8192MiB
	if err == nil {
		t.Fatal("checkFreeMemoryFromStats with insufficient memory = nil error, want error")
	}
}

func TestCheckFreeMemory_AllowsWhenAboveThreshold(t *testing.T) {
	t.Parallel()

	err := checkFreeMemoryFromStats(20000, 4096) // 20000MiB free, wants 4096MiB (needs 1.5x = 6144MiB)
	if err != nil {
		t.Errorf("checkFreeMemoryFromStats with ample memory = %v, want nil", err)
	}
}
