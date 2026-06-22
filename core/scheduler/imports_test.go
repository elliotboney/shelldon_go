package scheduler_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestReflexTierIsLLMFree enforces AC2 / AD-13: no package in the reflex tier —
// the scheduler itself and the reflex jobs it runs — may import the broker or the
// worker. Reflex jobs run in-core with no LLM call; a future accidental import of
// the LLM path fails the build, not merely a convention. It mirrors
// core/dispatch/imports_test.go. The test runs with its working directory set to
// core/scheduler/, so "../scheduler" and "../reflexes" are the trees to walk.
func TestReflexTierIsLLMFree(t *testing.T) {
	fset := token.NewFileSet()
	scanned := 0
	for _, tree := range []string{".", "../reflexes"} {
		err := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			scanned++
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(p, "/broker") || strings.Contains(p, "/worker") {
					t.Errorf("%s imports %q — reflex-tier jobs must never reach the LLM path (AC2, AD-13)", path, p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s tree: %v", tree, err)
		}
	}
	// Guard against a vacuous pass: if the walk found no source files (wrong CWD,
	// moved packages), the import check asserted nothing. scheduler.go + the two
	// reflex files mean at least 3 non-test files must be scanned.
	if scanned < 3 {
		t.Fatalf("scanned only %d source files — import-hygiene check is vacuous (expected ≥3)", scanned)
	}
}
