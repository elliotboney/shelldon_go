package memory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/memory"
)

// openTestCurated opens a Curated tree rooted at a fresh temp dir.
func openTestCurated(t *testing.T) (*memory.Curated, string) {
	t.Helper()
	root := t.TempDir()
	c, err := memory.OpenCurated(root)
	if err != nil {
		t.Fatalf("open curated: %v", err)
	}
	return c, root
}

// assertNoTempLeftover asserts dir's entries are exactly want (sorted) — catching
// an orphaned renameio temp file (named .<base>...) alongside the final files.
func assertNoTempLeftover(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != len(want) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir %s entries = %v, want exactly %v (orphaned temp file?)", dir, names, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("dir %s missing %q; entries = %v", dir, w, got)
		}
	}
}

// TestCurated_AtomicRoundTrip is AC1: about.md and facts/ files round-trip via the
// atomic writer and no renameio temp file is left behind. (Atomicity itself is
// covered by atomic_test.go; here we assert the curated layer wires it correctly.)
func TestCurated_AtomicRoundTrip(t *testing.T) {
	c, root := openTestCurated(t)

	if err := c.WriteAbout("hello"); err != nil {
		t.Fatalf("write about: %v", err)
	}
	got, err := c.ReadAbout()
	if err != nil {
		t.Fatalf("read about: %v", err)
	}
	if got != "hello" {
		t.Fatalf("about = %q, want hello", got)
	}

	if err := c.WriteFile("facts/pi.md", []byte("raspberry pi notes")); err != nil {
		t.Fatalf("write facts/pi.md: %v", err)
	}
	factGot, err := c.ReadFile("facts/pi.md")
	if err != nil {
		t.Fatalf("read facts/pi.md: %v", err)
	}
	if string(factGot) != "raspberry pi notes" {
		t.Fatalf("facts/pi.md = %q, want 'raspberry pi notes'", factGot)
	}

	factsDir := filepath.Join(root, "facts")
	if fi, err := os.Stat(factsDir); err != nil || !fi.IsDir() {
		t.Fatalf("facts/ dir missing: err=%v", err)
	}

	// Root holds only about.md + the facts/ dir — no leftover renameio temp.
	assertNoTempLeftover(t, root, "about.md", "facts")
	// facts/ holds only pi.md — no leftover temp.
	assertNoTempLeftover(t, factsDir, "pi.md")
}

// TestCurated_DirectiveAuthoritative is AC2: a DIRECTIVE.md placed by the OWNER
// (plain os.WriteFile, not the bot API) is returned by Directive() and placed FIRST
// under an authoritative header by AssembleContext.
func TestCurated_DirectiveAuthoritative(t *testing.T) {
	c, root := openTestCurated(t)

	const directiveText = "Be kind. Protect the household."
	if err := os.WriteFile(filepath.Join(root, "DIRECTIVE.md"), []byte(directiveText), 0o644); err != nil {
		t.Fatalf("owner write DIRECTIVE.md: %v", err)
	}

	dir, err := c.Directive()
	if err != nil {
		t.Fatalf("directive: %v", err)
	}
	if dir != directiveText {
		t.Fatalf("directive = %q, want %q", dir, directiveText)
	}

	const aboutText = "I am a desk pet."
	recent := []memory.Message{
		{Role: "owner", Content: "hi"},
		{Role: "pet", Content: "hello"},
	}
	out := memory.AssembleContext(dir, aboutText, recent)

	di := strings.Index(out, directiveText)
	ai := strings.Index(out, aboutText)
	if di < 0 || ai < 0 {
		t.Fatalf("assembled output missing sections: directive@%d about@%d\n%s", di, ai, out)
	}
	if di >= ai {
		t.Fatalf("directive must come before about: directive@%d about@%d\n%s", di, ai, out)
	}
	if !strings.Contains(out, "authoritative") {
		t.Fatalf("assembled output missing authoritative header:\n%s", out)
	}
	// Recent window present, oldest-first.
	hi := strings.Index(out, "owner: hi")
	hello := strings.Index(out, "pet: hello")
	if hi < 0 || hello < 0 || hi >= hello {
		t.Fatalf("recent window wrong: hi@%d hello@%d\n%s", hi, hello, out)
	}
}

// TestCurated_DisjointWriters is AC2 (structural): the bot may never write the
// owner's DIRECTIVE.md or the vault — those return ErrOwnerOnly and create nothing.
func TestCurated_DisjointWriters(t *testing.T) {
	c, root := openTestCurated(t)

	// Owner seeds a real DIRECTIVE.md; a bot write must not clobber it.
	const owned = "OWNER CONSTITUTION"
	if err := os.WriteFile(filepath.Join(root, "DIRECTIVE.md"), []byte(owned), 0o644); err != nil {
		t.Fatalf("owner write: %v", err)
	}
	if err := c.WriteFile("DIRECTIVE.md", []byte("BOT TRIES TO REWRITE")); !errors.Is(err, memory.ErrOwnerOnly) {
		t.Fatalf("WriteFile(DIRECTIVE.md) err = %v, want ErrOwnerOnly", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "DIRECTIVE.md"))
	if err != nil {
		t.Fatalf("read DIRECTIVE.md: %v", err)
	}
	if string(got) != owned {
		t.Fatalf("DIRECTIVE.md = %q, want it untouched (%q)", got, owned)
	}

	// vault/ is rejected and never created (no vault until Epic 5).
	if err := c.WriteFile("vault/secret.md", []byte("nope")); !errors.Is(err, memory.ErrOwnerOnly) {
		t.Fatalf("WriteFile(vault/secret.md) err = %v, want ErrOwnerOnly", err)
	}
	if _, err := os.Stat(filepath.Join(root, "vault")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("vault/ must not exist: stat err = %v", err)
	}
	// There is no DIRECTIVE writer at all on the curated tree (compile-time: Curated
	// exposes no Directive-write method); the bot's only path is WriteFile, already
	// proven to reject it above. The memory-op apply path is fenced separately in
	// TestApplyMemoryOps_CannotTargetDirective.
}

// TestAssembleContext_TrailingNewlinesDoNotCompound proves trailing whitespace in
// section content (real about.md/DIRECTIVE.md files end in a newline) collapses to
// the single blank-line separator, not stacked blank lines.
func TestAssembleContext_TrailingNewlinesDoNotCompound(t *testing.T) {
	out := memory.AssembleContext("be kind\n\n", "a shellfish\n", nil)
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("assembled context has stacked blank lines:\n%q", out)
	}
	// Sections are still present and separated by exactly one blank line.
	if !strings.Contains(out, "be kind") || !strings.Contains(out, "a shellfish") {
		t.Fatalf("assembled context dropped content: %q", out)
	}
}

// TestApplyMemoryOps_CannotTargetDirective proves the other half of the disjoint
// writer set (AC2): no memory-op kind writes DIRECTIVE — an invented op is skipped
// as a no-op (4.2's unknown-kind behavior), so the worker's proposal channel can
// never reach the owner's constitution.
func TestApplyMemoryOps_CannotTargetDirective(t *testing.T) {
	s, err := memory.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ops := []contracts.MemoryOp{{Kind: "rewrite_directive", Observation: "x"}}
	if err := s.ApplyMemoryOps(context.Background(), ops); err != nil {
		t.Fatalf("ApplyMemoryOps(unknown kind) = %v, want nil (skipped)", err)
	}
}

// TestCurated_GracefulAbsent: a fresh tree has no about/DIRECTIVE — reads return
// empty without error, and an all-empty assembly is empty.
func TestCurated_GracefulAbsent(t *testing.T) {
	c, _ := openTestCurated(t)

	about, err := c.ReadAbout()
	if err != nil {
		t.Fatalf("read absent about: %v", err)
	}
	if about != "" {
		t.Fatalf("absent about = %q, want empty", about)
	}

	dir, err := c.Directive()
	if err != nil {
		t.Fatalf("read absent directive: %v", err)
	}
	if dir != "" {
		t.Fatalf("absent directive = %q, want empty", dir)
	}

	if out := memory.AssembleContext("", "", nil); strings.TrimSpace(out) != "" {
		t.Fatalf("AssembleContext(empty) = %q, want empty", out)
	}
}

// TestCurated_PathSafety: traversal/escape paths are rejected on both read and
// write, and no file is created outside the root.
func TestCurated_PathSafety(t *testing.T) {
	c, root := openTestCurated(t)

	if err := c.WriteFile("../escape.md", []byte("nope")); err == nil {
		t.Fatalf("WriteFile(../escape.md) = nil, want rejection")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escape.md must not exist outside root: stat err = %v", err)
	}

	if _, err := c.ReadFile("../../etc/x"); err == nil {
		t.Fatalf("ReadFile(../../etc/x) = nil, want rejection")
	}

	// An absolute path is also rejected.
	if err := c.WriteFile("/tmp/abs.md", []byte("nope")); err == nil {
		t.Fatalf("WriteFile(/tmp/abs.md) = nil, want rejection")
	}
}
