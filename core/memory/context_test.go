package memory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elliotboney/shelldon_go/core/memory"
)

// openTestContext wires a fresh Store + Curated into a Context with the given recentN.
// It returns the curated root so callers can write owner files (DIRECTIVE.md) directly.
func openTestContext(t *testing.T, recentN int) (*memory.Context, string) {
	t.Helper()
	store, err := memory.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	curatedRoot := t.TempDir()
	curated, err := memory.OpenCurated(curatedRoot)
	if err != nil {
		t.Fatalf("open curated: %v", err)
	}

	return memory.NewContext(store, curated, recentN), curatedRoot
}

// TestPromptContext_ContainsAllSections verifies the happy path: directive before
// about, about present, messages in oldest→newest order.
func TestPromptContext_ContainsAllSections(t *testing.T) {
	ctx := context.Background()

	store, err := memory.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	curatedRoot := t.TempDir()
	curated, err := memory.OpenCurated(curatedRoot)
	if err != nil {
		t.Fatalf("open curated for seeding: %v", err)
	}

	// Write owner-only DIRECTIVE.md directly (Curated.WriteFile guards this path).
	if err := os.WriteFile(filepath.Join(curatedRoot, "DIRECTIVE.md"), []byte("be kind"), 0o644); err != nil {
		t.Fatalf("write DIRECTIVE.md: %v", err)
	}
	if err := curated.WriteAbout("a small shellfish"); err != nil {
		t.Fatalf("write about: %v", err)
	}
	if _, err := store.Append(ctx, "c1", "owner", "hello there"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := store.Append(ctx, "c1", "pet", "hi friend"); err != nil {
		t.Fatalf("append: %v", err)
	}

	out, err := memory.NewContext(store, curated, 10).PromptContext(ctx, "c1")
	if err != nil {
		t.Fatalf("PromptContext: %v", err)
	}

	// Directive must appear before about.
	dIdx := strings.Index(out, "be kind")
	aIdx := strings.Index(out, "a small shellfish")
	if dIdx < 0 {
		t.Errorf("output missing directive text %q:\n%s", "be kind", out)
	}
	if aIdx < 0 {
		t.Errorf("output missing about text %q:\n%s", "a small shellfish", out)
	}
	if dIdx >= 0 && aIdx >= 0 && dIdx > aIdx {
		t.Errorf("directive (%d) must appear before about (%d) in output:\n%s", dIdx, aIdx, out)
	}

	// Messages must be oldest→newest.
	helloIdx := strings.Index(out, "hello there")
	hiIdx := strings.Index(out, "hi friend")
	if helloIdx < 0 {
		t.Errorf("output missing first message %q:\n%s", "hello there", out)
	}
	if hiIdx < 0 {
		t.Errorf("output missing second message %q:\n%s", "hi friend", out)
	}
	if helloIdx >= 0 && hiIdx >= 0 && helloIdx > hiIdx {
		t.Errorf("'hello there' (%d) must appear before 'hi friend' (%d) (oldest→newest):\n%s", helloIdx, hiIdx, out)
	}
}

// TestPromptContext_EmptyStoreAndCurated verifies that a brand-new store with no
// messages and an empty curated tree yields an empty string (all sections omitted).
func TestPromptContext_EmptyStoreAndCurated(t *testing.T) {
	ctx := context.Background()
	mctx, _ := openTestContext(t, 10)

	out, err := mctx.PromptContext(ctx, "nope")
	if err != nil {
		t.Fatalf("PromptContext on empty store: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for empty store+curated, got:\n%s", out)
	}
}

// TestPromptContext_LearningsSection verifies that a promoted learning written to
// FactsLearningsPath appears under "### LEARNINGS" in PromptContext output, ordered
// DIRECTIVE → ABOUT → LEARNINGS → RECENT.
func TestPromptContext_LearningsSection(t *testing.T) {
	ctx := context.Background()

	store, err := memory.Open(filepath.Join(t.TempDir(), "learn.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	curatedRoot := t.TempDir()
	curated, err := memory.OpenCurated(curatedRoot)
	if err != nil {
		t.Fatalf("open curated: %v", err)
	}

	if err := os.WriteFile(filepath.Join(curatedRoot, "DIRECTIVE.md"), []byte("be kind"), 0o644); err != nil {
		t.Fatalf("write DIRECTIVE.md: %v", err)
	}
	if err := curated.WriteAbout("a small shellfish"); err != nil {
		t.Fatalf("write about: %v", err)
	}
	if err := curated.AppendFact(memory.FactsLearningsPath, "always greet with enthusiasm"); err != nil {
		t.Fatalf("append fact: %v", err)
	}
	if _, err := store.Append(ctx, "c1", "owner", "hey"); err != nil {
		t.Fatalf("append message: %v", err)
	}

	out, err := memory.NewContext(store, curated, 10).PromptContext(ctx, "c1")
	if err != nil {
		t.Fatalf("PromptContext: %v", err)
	}

	if !strings.Contains(out, "### LEARNINGS") {
		t.Errorf("output missing ### LEARNINGS section:\n%s", out)
	}
	if !strings.Contains(out, "always greet with enthusiasm") {
		t.Errorf("output missing promoted learning text:\n%s", out)
	}

	// Order: DIRECTIVE → ABOUT → LEARNINGS → RECENT
	dIdx := strings.Index(out, "be kind")
	aIdx := strings.Index(out, "a small shellfish")
	lIdx := strings.Index(out, "### LEARNINGS")
	rIdx := strings.Index(out, "### RECENT")

	if dIdx < 0 || aIdx < 0 || lIdx < 0 || rIdx < 0 {
		t.Fatalf("section missing: directive@%d about@%d learnings@%d recent@%d\n%s", dIdx, aIdx, lIdx, rIdx, out)
	}
	if dIdx > aIdx || aIdx > lIdx || lIdx > rIdx {
		t.Errorf("wrong order: directive@%d about@%d learnings@%d recent@%d\n%s", dIdx, aIdx, lIdx, rIdx, out)
	}
}

// TestPromptContext_RecentNCap verifies that recentN=1 with two messages returns
// only the most-recent message ("hi friend"), not the older one ("hello there").
func TestPromptContext_RecentNCap(t *testing.T) {
	ctx := context.Background()

	store, err := memory.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	curated, err := memory.OpenCurated(t.TempDir())
	if err != nil {
		t.Fatalf("open curated: %v", err)
	}

	if _, err := store.Append(ctx, "c1", "owner", "hello there"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := store.Append(ctx, "c1", "pet", "hi friend"); err != nil {
		t.Fatalf("append: %v", err)
	}

	mctx := memory.NewContext(store, curated, 1)
	out, err := mctx.PromptContext(ctx, "c1")
	if err != nil {
		t.Fatalf("PromptContext: %v", err)
	}

	if strings.Contains(out, "hello there") {
		t.Errorf("recentN=1 should exclude older message 'hello there', got:\n%s", out)
	}
	if !strings.Contains(out, "hi friend") {
		t.Errorf("recentN=1 should include most-recent 'hi friend', got:\n%s", out)
	}
}
