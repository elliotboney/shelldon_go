package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/renameio/v2"
)

const perm = 0o644

// assertOnlyFile asserts dir contains exactly one entry named name — catching
// both an orphaned temp file and a partial file left at the target path.
func assertOnlyFile(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir entries = %v, want exactly [%q] (orphaned temp or partial file)", names, name)
	}
}

// TestWriteAtomic_ReplacesAtomically is the happy path: a second write replaces
// the file fully and leaves no temp behind.
func TestWriteAtomic_ReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "about.md")

	if err := WriteAtomic(path, []byte("ORIGINAL"), perm); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := WriteAtomic(path, []byte("UPDATED"), perm); err != nil {
		t.Fatalf("update write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "UPDATED" {
		t.Fatalf("content = %q, want UPDATED", got)
	}
	assertOnlyFile(t, dir, "about.md")
}

// TestWriteAtomic_CrashSafety is the required M0 atomic-write crash-safety test
// (AD-7/AD-10/NFR11): a write interrupted before the atomic rename commits leaves
// the prior file intact — no partial/corrupt file is observable at the target.
// A real rename(2) is atomic at the OS level, so the failure mode to exercise is
// "process dies after writing the temp, before the rename commits" — modeled by
// aborting renameio's PendingFile with Cleanup() instead of CloseAtomicallyReplace().
func TestWriteAtomic_CrashSafety(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "about.md")

	if err := WriteAtomic(path, []byte("ORIGINAL"), perm); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	pf, err := renameio.NewPendingFile(path)
	if err != nil {
		t.Fatalf("pending file: %v", err)
	}
	if _, err := pf.Write([]byte("PARTIAL / TORN CONTENT")); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	// Abort BEFORE the rename commits — models a crash mid-write.
	if err := pf.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("prior file must still exist: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Fatalf("target = %q, want ORIGINAL — interrupted write corrupted the prior file", got)
	}
	assertOnlyFile(t, dir, "about.md")
}
