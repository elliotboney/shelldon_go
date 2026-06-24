package memory

import (
	"context"
	"fmt"
	"log/slog"
)

// Context assembles the AD-7 retrieval context for a single conversation turn.
// It is the read-only composition layer (AD-6): it pulls live data from the
// Store and Curated tree, then hands the assembled string to the worker. LLM
// grep / FTS5 augmentation arrives in a later story.
type Context struct {
	store   *Store
	curated *Curated
	recentN int
}

// NewContext returns a Context that will pull up to recentN messages from store
// and read owner/bot files from curated.
func NewContext(store *Store, curated *Curated, recentN int) *Context {
	return &Context{store: store, curated: curated, recentN: recentN}
}

// PromptContext builds the AD-7 retrieval string for convoID. It structurally
// satisfies a one-method interface defined in the worker package — the exact
// signature must not change. The order is: owner directive, bot about, then the
// recent window rendered oldest→newest (Recent returns most-recent-first; we
// reverse into a local copy so the assembled prompt reads chronologically).
func (c *Context) PromptContext(ctx context.Context, convoID string) (string, error) {
	// Curated reads are best-effort — a missing file yields "" (no error); a real
	// I/O error (disk/permissions) omits that section but is logged, not silent (AD-17).
	directive, derr := c.curated.Directive()
	if derr != nil {
		slog.Warn("memory: read DIRECTIVE failed; omitting from prompt context", "err", derr)
	}
	about, aerr := c.curated.ReadAbout()
	if aerr != nil {
		slog.Warn("memory: read about failed; omitting from prompt context", "err", aerr)
	}

	recent, err := c.store.Recent(ctx, convoID, c.recentN)
	if err != nil {
		return "", fmt.Errorf("memory: prompt context: %w", err)
	}

	// Reverse into a new slice (oldest→newest) without mutating the returned slice.
	reversed := make([]Message, len(recent))
	for i, m := range recent {
		reversed[len(recent)-1-i] = m
	}

	return AssembleContext(directive, about, reversed), nil
}
