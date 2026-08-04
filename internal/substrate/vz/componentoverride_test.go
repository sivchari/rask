//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/components"
	"github.com/sivchari/rask/internal/guestlayout"
)

func TestStageComponentOverride_NilPathsIsNoOp(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	if err := stageComponentOverride(dataDir, nil); err != nil {
		t.Fatalf("stageComponentOverride: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, componentOverrideSubdir)); !os.IsNotExist(err) {
		t.Errorf("stageComponentOverride(nil) created %s, want nothing staged", componentOverrideSubdir)
	}
}

// writeFakeComponentDir creates dir/name for every name in names with
// placeholder content, mimicking what a caller's --component-dir would
// contain (see components.LocalDirSource).
func writeFakeComponentDir(t *testing.T, dir string, names ...string) {
	t.Helper()

	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"-content"), 0o755); err != nil {
			t.Fatalf("writing fake component %s: %v", name, err)
		}
	}
}

// fakeResolvedComponentPaths writes all five components.LocalDirSource
// override binaries into a fresh directory and returns the *components.Paths
// stageComponentOverride expects — a real components.LocalDirSource.Resolve()
// call always populates every one of these five fields together (validated
// up front, see LocalDirSource.validate), so tests exercising
// stageComponentOverride/buildComponentOverlayCpio must too.
func fakeResolvedComponentPaths(t *testing.T) *components.Paths {
	t.Helper()

	componentDir := t.TempDir()
	names := []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler", "kubelet", "kubectl"}
	writeFakeComponentDir(t, componentDir, names...)

	return &components.Paths{
		KubeAPIServer:         filepath.Join(componentDir, "kube-apiserver"),
		KubeControllerManager: filepath.Join(componentDir, "kube-controller-manager"),
		KubeScheduler:         filepath.Join(componentDir, "kube-scheduler"),
		Kubelet:               filepath.Join(componentDir, "kubelet"),
		Kubectl:               filepath.Join(componentDir, "kubectl"),
	}
}

func TestStageComponentOverride_CopiesAllFiveBinaries(t *testing.T) {
	t.Parallel()

	paths := fakeResolvedComponentPaths(t)

	dataDir := t.TempDir()

	if err := stageComponentOverride(dataDir, paths); err != nil {
		t.Fatalf("stageComponentOverride: %v", err)
	}

	names := []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler", "kubelet", "kubectl"}
	for _, name := range names {
		got, err := os.ReadFile(filepath.Join(dataDir, componentOverrideSubdir, name))
		if err != nil {
			t.Fatalf("reading staged %s: %v", name, err)
		}

		if string(got) != name+"-content" {
			t.Errorf("staged %s content = %q, want %q", name, got, name+"-content")
		}
	}
}

func TestBuildComponentOverlayCpio_NothingStagedReturnsEmptyArchive(t *testing.T) {
	t.Parallel()

	data, err := buildComponentOverlayCpio(t.TempDir())
	if err != nil {
		t.Fatalf("buildComponentOverlayCpio: %v", err)
	}

	if len(data) != 0 {
		t.Errorf("buildComponentOverlayCpio() with nothing staged = %d bytes, want 0", len(data))
	}
}

func TestBuildComponentOverlayCpio_PlacesBinariesAtGuestBinDir(t *testing.T) {
	t.Parallel()

	paths := fakeResolvedComponentPaths(t)

	dataDir := t.TempDir()
	if err := stageComponentOverride(dataDir, paths); err != nil {
		t.Fatalf("stageComponentOverride: %v", err)
	}

	data, err := buildComponentOverlayCpio(dataDir)
	if err != nil {
		t.Fatalf("buildComponentOverlayCpio: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("buildComponentOverlayCpio() with a staged override = empty archive, want non-empty")
	}

	extracted := extractArchiveForTest(t, data)

	guestRelPath := strings.TrimPrefix(guestlayout.BinDir, "/") + "/kubectl"

	got, err := os.ReadFile(filepath.Join(extracted, guestRelPath))
	if err != nil {
		t.Fatalf("reading extracted component override: %v", err)
	}

	if string(got) != "kubectl-content" {
		t.Errorf("extracted kubectl content = %q, want %q", got, "kubectl-content")
	}
}

// A "does concatenating two cpio archives with a duplicate path make the
// later one win" test is deliberately NOT implemented against
// /usr/bin/cpio here: macOS's bundled cpio (a bsdcpio/libarchive build)
// stops at the first TRAILER!!! entry and never even looks at a second,
// concatenated archive at all (confirmed empirically during this session —
// it is not a valid stand-in for the Linux kernel's own initramfs
// unpacker, unlike its use elsewhere in this package to validate newc
// format compliance in isolation). The actual target — the Linux kernel's
// init/initramfs.c — was instead confirmed directly from its current
// mainline source: do_name() unconditionally opens a regular-file entry
// with O_CREAT|O_TRUNC (clean_path only unlinks first if an existing path's
// *type* differs) and then vfs_truncate()s it to the new entry's body_len,
// so a later archive's entry at an already-existing regular-file path
// genuinely overwrites it rather than erroring or being skipped. The
// end-to-end behavior is verified by a real guest kernel boot instead — see
// PROGRESS-vz-seams.md's --component-dir E2E run.
