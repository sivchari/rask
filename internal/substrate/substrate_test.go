package substrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStagePrebootFiles_CopiesToDataDirPrebootDest(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "webhook-kubeconfig.yaml")

	if err := os.WriteFile(srcPath, []byte("kind: Config\n"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dataDir := t.TempDir()

	files := []PrebootFile{
		{Src: srcPath, Dest: "auth/webhook.yaml"},
	}

	if err := StagePrebootFiles(dataDir, files); err != nil {
		t.Fatalf("StagePrebootFiles: %v", err)
	}

	want := filepath.Join(dataDir, PrebootSubdir, "auth", "webhook.yaml")

	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading staged file at %s: %v", want, err)
	}

	if string(got) != "kind: Config\n" {
		t.Errorf("staged content = %q, want %q", got, "kind: Config\n")
	}
}

func TestStagePrebootFiles_NoFilesIsNoop(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	if err := StagePrebootFiles(dataDir, nil); err != nil {
		t.Fatalf("StagePrebootFiles: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, PrebootSubdir)); !os.IsNotExist(err) {
		t.Errorf("preboot dir was created despite no files given: err = %v", err)
	}
}

func TestStagePrebootFiles_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	srcPath := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(srcPath, []byte("evil"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dataDir := t.TempDir()

	tests := []struct {
		name string
		dest string
	}{
		{"parent traversal", "../../etc/passwd"},
		{"absolute path", "/etc/passwd"},
		{"empty dest", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := StagePrebootFiles(dataDir, []PrebootFile{{Src: srcPath, Dest: tt.dest}})
			if err == nil {
				t.Fatalf("StagePrebootFiles(dest=%q) = nil error, want error", tt.dest)
			}
		})
	}
}

func TestStagePrebootFiles_MissingSrcFailsWithClearError(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	err := StagePrebootFiles(dataDir, []PrebootFile{{Src: filepath.Join(t.TempDir(), "missing"), Dest: "dest.txt"}})
	if err == nil {
		t.Fatal("StagePrebootFiles with a missing src = nil error, want error")
	}
}

func TestPrebootFilePath_MatchesWhereStagePrebootFilesActuallyWrites(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "webhook-kubeconfig.yaml")

	if err := os.WriteFile(srcPath, []byte("kind: Config\n"), 0o644); err != nil {
		t.Fatalf("writing src: %v", err)
	}

	dataDir := t.TempDir()
	dest := "auth/webhook.yaml"

	if err := StagePrebootFiles(dataDir, []PrebootFile{{Src: srcPath, Dest: dest}}); err != nil {
		t.Fatalf("StagePrebootFiles: %v", err)
	}

	// A Runtime.PrebootPath implementation would call PrebootFilePath with
	// its own substrate-specific base directory; hostproc's happens to be
	// exactly filepath.Join(dataDir, PrebootSubdir), so this doubles as a
	// direct check that the join formula stays byte-for-byte in sync with
	// StagePrebootFiles' own.
	got := PrebootFilePath(filepath.Join(dataDir, PrebootSubdir), dest)

	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("PrebootFilePath() = %q, does not match where StagePrebootFiles wrote: %v", got, err)
	}

	if string(content) != "kind: Config\n" {
		t.Errorf("content at PrebootFilePath() = %q, want %q", content, "kind: Config\n")
	}
}

func TestPrebootFilePath_JoinsSlashSeparatedDestRegardlessOfHostSeparator(t *testing.T) {
	t.Parallel()

	got := PrebootFilePath("/base", "a/b/c.txt")
	want := filepath.Join("/base", "a", "b", "c.txt")

	if got != want {
		t.Errorf("PrebootFilePath() = %q, want %q", got, want)
	}
}

func TestPrebootFilePath_DoesNotValidateDest(t *testing.T) {
	t.Parallel()

	// Unlike StagePrebootFiles, PrebootFilePath has no error return to
	// reject an invalid dest through (see its doc comment) — it still
	// resolves to a path rather than panicking or erroring.
	got := PrebootFilePath("/base", "../escapes")
	want := filepath.Join("/base", "../escapes")

	if got != want {
		t.Errorf("PrebootFilePath() = %q, want %q", got, want)
	}
}
