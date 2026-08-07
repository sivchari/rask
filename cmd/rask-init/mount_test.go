//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureStickyDir_CreatesWithStickyBit guards against the regression
// this function exists to fix: os.MkdirAll alone does not reliably set
// special bits like the sticky bit (subject to umask), which is what
// mountBase relies on ensureStickyDir for when creating /tmp (see that
// call's doc comment for the "kubectl exec" failure a missing/wrong-mode
// /tmp causes).
func TestEnsureStickyDir_CreatesWithStickyBit(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "tmp")

	if err := ensureStickyDir(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("ensureStickyDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}

	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}

	if mode := info.Mode(); mode.Perm() != 0o777 || mode&os.ModeSticky == 0 {
		t.Errorf("mode = %v, want perm 0777 with sticky bit set", mode)
	}
}

// TestEnsureStickyDir_RawOctalDoesNotSetStickyBit documents the exact
// footgun ensureStickyDir's own doc comment warns callers about: a raw
// traditional-octal literal like 0o1777 does NOT set fs.FileMode's sticky
// bit (that bit lives at a different position in fs.FileMode than in a Unix
// mode_t — see ensureStickyDir's doc comment), so passing it produces a
// plain 0o777 directory with no error. This test exists so a future change
// that "simplifies" mountBase's /tmp call back to a raw 0o1777 literal
// fails loudly instead of silently reintroducing the original bug.
func TestEnsureStickyDir_RawOctalDoesNotSetStickyBit(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "tmp")

	if err := ensureStickyDir(dir, 0o1777); err != nil {
		t.Fatalf("ensureStickyDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}

	if mode := info.Mode(); mode&os.ModeSticky != 0 {
		t.Errorf("mode = %v: raw 0o1777 unexpectedly set the sticky bit", mode)
	}
}

// TestEnsureStickyDir_FixesModeOnExistingDir covers the case mountBase's
// second, post-switchRoot call depends on: switchRoot's copyTree recreates
// directories via info.Mode().Perm(), which strips special bits, so a
// pre-existing /tmp copied without the sticky bit must still end up with it
// set after ensureStickyDir runs again.
func TestEnsureStickyDir_FixesModeOnExistingDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "tmp")

	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	if err := ensureStickyDir(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("ensureStickyDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}

	if mode := info.Mode(); mode&os.ModeSticky == 0 {
		t.Errorf("mode = %v, want sticky bit set", mode)
	}
}
