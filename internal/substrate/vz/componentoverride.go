//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestlayout"
)

// componentOverrideSubdir is where Runtime.Start stages opts.ComponentDir's
// five override binaries (see stageComponentOverride), for RunVMHost — a
// separate process — to read back and layer onto the shared template
// initramfs (see buildComponentOverlayCpio). Mirrors PrebootFiles' own
// staging pattern for the same reason: Runtime.Start and RunVMHost only
// share dataDir, not memory.
const componentOverrideSubdir = "component-override"

// stageComponentOverride copies opts.ComponentDir's five overridden binary
// paths (from an already-Resolve()d components.Paths — see
// components.LocalDirSource) into dataDir/componentOverrideSubdir, so
// RunVMHost can find them without needing opts itself. A no-op if paths is
// nil (Runtime.Start only calls this when opts.ComponentDir != "").
func stageComponentOverride(dataDir string, paths *components.Paths) error {
	if paths == nil {
		return nil
	}

	destDir := filepath.Join(dataDir, componentOverrideSubdir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("vz: creating %s: %w", destDir, err)
	}

	overrides := map[string]string{
		"kube-apiserver":          paths.KubeAPIServer,
		"kube-controller-manager": paths.KubeControllerManager,
		"kube-scheduler":          paths.KubeScheduler,
		"kubelet":                 paths.Kubelet,
		"kubectl":                 paths.Kubectl,
	}

	for name, src := range overrides {
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("vz: reading component-dir override %s: %w", src, err)
		}

		if err := os.WriteFile(filepath.Join(destDir, name), data, 0o755); err != nil {
			return fmt.Errorf("vz: staging component-dir override %s: %w", name, err)
		}
	}

	return nil
}

// buildComponentOverlayCpio returns a cpio archive placing every binary
// stageComponentOverride staged under dataDir at its normal
// guestlayout.BinDir location — the same path the shared template
// initramfs already placed rask's own default build of that binary at —
// so that once this overlay is concatenated after the template (see
// concatInitramfs), the kernel's initramfs unpacker overwrites the
// template's copy with this one.
//
// Confirmed directly from the Linux kernel's init/initramfs.c (mainline,
// checked live during this session), not assumed: do_name() opens a
// regular-file entry with O_CREAT|O_TRUNC unconditionally (clean_path only
// unlinks an existing path first if its *type* differs, e.g. a directory
// where a file is expected) and then vfs_truncate()s it to the new entry's
// body_len before copying the new body in — so a later archive's entry at
// an already-populated regular-file path genuinely replaces its content,
// it does not error, get skipped, or leave stale trailing bytes from a
// longer prior entry. TRAILER!!! (do_name's other branch) does not stop
// the unpacker either; it only resets the parser to look for another
// header in the remaining bytes, which is what makes concatenating
// multiple complete archives (each with its own TRAILER!!!) into one initrd
// work at all — see concatInitramfs's own doc comment. End-to-end verified
// with a real guest kernel boot too — see PROGRESS-vz-seams.md's
// --component-dir E2E run.
//
// Returns a zero-length (but non-nil) slice if nothing was staged (no
// --component-dir was passed), so RunVMHost can skip concatenation
// entirely in the common case, matching buildPrebootCpio's contract.
func buildComponentOverlayCpio(dataDir string) ([]byte, error) {
	overrideDir := filepath.Join(dataDir, componentOverrideSubdir)

	entries, err := os.ReadDir(overrideDir)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("vz: reading staged component overrides %s: %w", overrideDir, err)
	}

	if len(entries) == 0 {
		return []byte{}, nil
	}

	w := newCpioWriter()
	guestPrefix := strings.TrimPrefix(guestlayout.BinDir, "/")

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(overrideDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("vz: reading staged component override %s: %w", e.Name(), err)
		}

		if err := w.WriteFile(path.Join(guestPrefix, e.Name()), 0o755, data); err != nil {
			return nil, fmt.Errorf("vz: writing component override %s into overlay: %w", e.Name(), err)
		}
	}

	return w.Finish()
}
