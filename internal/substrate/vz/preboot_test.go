//go:build darwin

package vz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sivchari/rask/internal/guestlayout"
	"github.com/sivchari/rask/internal/substrate"
)

func TestBuildPrebootCpio_NoStagedFilesReturnsEmptyArchive(t *testing.T) {
	t.Parallel()

	data, err := buildPrebootCpio(t.TempDir())
	if err != nil {
		t.Fatalf("buildPrebootCpio: %v", err)
	}

	if len(data) != 0 {
		t.Errorf("buildPrebootCpio() with nothing staged = %d bytes, want 0", len(data))
	}
}

func TestBuildPrebootCpio_IncludesStagedFilesAtGuestPrebootStagingDir(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	srcPath := filepath.Join(t.TempDir(), "webhook.yaml")
	if err := os.WriteFile(srcPath, []byte("kind: Config\n"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	if err := substrate.StagePrebootFiles(dataDir, []substrate.PrebootFile{{Src: srcPath, Dest: "auth/webhook.yaml"}}); err != nil {
		t.Fatalf("StagePrebootFiles: %v", err)
	}

	data, err := buildPrebootCpio(dataDir)
	if err != nil {
		t.Fatalf("buildPrebootCpio: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("buildPrebootCpio() with a staged file = empty archive, want non-empty")
	}

	extracted := extractArchiveForTest(t, data)

	guestRelPath := strings.TrimPrefix(guestlayout.PrebootStagingDir, "/") + "/auth/webhook.yaml"

	got, err := os.ReadFile(filepath.Join(extracted, guestRelPath))
	if err != nil {
		t.Fatalf("reading extracted preboot file: %v", err)
	}

	if string(got) != "kind: Config\n" {
		t.Errorf("extracted content = %q, want %q", got, "kind: Config\n")
	}
}

func TestRuntime_PrebootPathUsesGuestPrebootDirNotStagingDir(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), nil)

	dest := "auth/webhook.yaml"
	got := r.PrebootPath("dev", dest)

	// guestlayout.PrebootDir, not PrebootStagingDir: rask-init's
	// copyPrebootFiles moves staged content there once the data disk is
	// mounted (see guestlayout.PrebootStagingDir's doc comment), so
	// PrebootDir is what a component the guest launches — e.g.
	// kube-apiserver, via an ExtraAPIServerArgs flag — actually reads at
	// runtime.
	want := filepath.Join(guestlayout.PrebootDir, dest)
	if got != want {
		t.Errorf("PrebootPath() = %q, want %q", got, want)
	}

	if strings.Contains(got, guestlayout.PrebootStagingDir) {
		t.Errorf("PrebootPath() = %q, must not point at the staging dir %q: nothing reads from there once the data disk is mounted", got, guestlayout.PrebootStagingDir)
	}
}

func TestRuntime_PrebootPathDoesNotVaryByClusterName(t *testing.T) {
	t.Parallel()

	r := New(t.TempDir(), nil)

	dest := "auth/webhook.yaml"

	// Unlike hostproc's host path (scoped by homeDir/name, since one host
	// stages every cluster's files under a shared filesystem), vz's guest
	// path is fixed: each VM only ever sees its own guest filesystem, so
	// there is nothing for a cluster name to disambiguate.
	if got1, got2 := r.PrebootPath("dev", dest), r.PrebootPath("staging", dest); got1 != got2 {
		t.Errorf("PrebootPath() varies by cluster name: %q vs %q, want identical", got1, got2)
	}
}

func TestConcatInitramfs_AppendsExtraAfterTemplate(t *testing.T) {
	t.Parallel()

	templatePath := filepath.Join(t.TempDir(), "template.cpio")
	templateContent := []byte("template-archive-bytes")

	if err := os.WriteFile(templatePath, templateContent, 0o644); err != nil {
		t.Fatalf("writing template: %v", err)
	}

	extra := []byte("preboot-overlay-bytes")

	destPath := filepath.Join(t.TempDir(), "combined.cpio")

	if err := concatInitramfs(destPath, templatePath, extra); err != nil {
		t.Fatalf("concatInitramfs: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading combined initramfs: %v", err)
	}

	want := append(append([]byte{}, templateContent...), extra...)
	if string(got) != string(want) {
		t.Errorf("combined initramfs = %q, want %q", got, want)
	}
}
