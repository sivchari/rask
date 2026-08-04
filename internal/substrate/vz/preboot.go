//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate"
)

// buildPrebootCpio builds a small cpio archive containing every file
// Runtime.Start staged under dataDir/substrate.PrebootSubdir (see
// substrate.StagePrebootFiles), placed at their guest-side destination
// under guestlayout.PrebootStagingDir — NOT guestlayout.PrebootDir itself;
// see that constant's doc comment for why (cmd/rask-init's
// copyPrebootFiles is what moves this staging content into PrebootDir,
// once the data disk is mounted). It returns a zero-length (but non-nil)
// slice if nothing was staged, so a caller can skip initramfs concatenation
// entirely in the overwhelmingly common case of no preboot files by
// checking len() rather than a nil archive.
func buildPrebootCpio(dataDir string) ([]byte, error) {
	prebootDir := filepath.Join(dataDir, substrate.PrebootSubdir)

	entries, err := os.ReadDir(prebootDir)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("vz: reading staged preboot dir %s: %w", prebootDir, err)
	}

	if len(entries) == 0 {
		return []byte{}, nil
	}

	w := newCpioWriter()
	guestPrefix := strings.TrimPrefix(guestlayout.PrebootStagingDir, "/")

	err = filepath.WalkDir(prebootDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(prebootDir, p)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}

		return w.WriteFile(path.Join(guestPrefix, filepath.ToSlash(rel)), 0o600, data)
	})
	if err != nil {
		return nil, fmt.Errorf("vz: building preboot cpio: %w", err)
	}

	return w.Finish()
}

// concatInitramfs writes templatePath's content followed by extra to
// destPath: the newc cpio format lets multiple archives be concatenated
// into a single initrd (see cpio.go's cpioTrailerName doc comment) — the
// kernel's initramfs unpacker keeps scanning for another archive header
// immediately after each TRAILER!!! entry rather than stopping at EOF, so
// this "preboot overlay" archive's files land in the guest's filesystem
// alongside everything the template initramfs already provides.
func concatInitramfs(destPath, templatePath string, extra []byte) error {
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("vz: reading template initramfs %s: %w", templatePath, err)
	}

	combined := make([]byte, 0, len(template)+len(extra))
	combined = append(combined, template...)
	combined = append(combined, extra...)

	if err := os.WriteFile(destPath, combined, 0o644); err != nil {
		return fmt.Errorf("vz: writing combined initramfs %s: %w", destPath, err)
	}

	return nil
}
