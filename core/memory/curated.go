package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Curated is the AD-7 curated markdown tree: a bot-owned directory of small
// markdown files (about.md, facts/…) that core writes atomically (NFR11, via
// WriteAtomic). It deliberately enforces disjoint writer sets — DIRECTIVE.md is the
// owner's sole-writer constitution, read-only to core, and the vault is not yet
// born (Epic 5) — so the bot can never write either through this API.
type Curated struct {
	root string
}

// ErrOwnerOnly is returned when a write targets an owner-only path the bot may
// never write: the DIRECTIVE.md constitution or the (future) vault.
var ErrOwnerOnly = errors.New("memory: path is owner-only, not bot-writable")

// OpenCurated opens (creating if absent) the curated tree rooted at root, plus its
// facts/ subdir. An empty root is rejected outright.
func OpenCurated(root string) (*Curated, error) {
	if root == "" {
		return nil, fmt.Errorf("memory: empty curated root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("memory: create curated root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "facts"), 0o755); err != nil {
		return nil, fmt.Errorf("memory: create facts dir: %w", err)
	}
	return &Curated{root: root}, nil
}

// escapeClean cleans relPath and rejects any path that escapes the curated root
// (traversal or absolute). It returns the cleaned, root-relative path.
func escapeClean(relPath string) (string, error) {
	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("memory: path escapes curated root: %q", relPath)
	}
	return clean, nil
}

// WriteFile atomically writes data to relPath within the curated tree (NFR11).
// Guards run in order: reject root-escaping paths; reject the owner-only
// DIRECTIVE.md and vault (disjoint writers — ErrOwnerOnly, nothing created); then
// ensure the parent dir exists and write atomically.
func (c *Curated) WriteFile(relPath string, data []byte) error {
	clean, err := escapeClean(relPath)
	if err != nil {
		return err
	}
	// Disjoint writers: the bot may never write the owner's constitution or the
	// not-yet-existent vault. Reject before creating anything.
	if clean == "DIRECTIVE.md" ||
		clean == "vault" || strings.HasPrefix(clean, "vault"+string(os.PathSeparator)) {
		return ErrOwnerOnly
	}
	join := filepath.Join(c.root, clean)
	if err := os.MkdirAll(filepath.Dir(join), 0o755); err != nil {
		return fmt.Errorf("memory: create dir for %q: %w", relPath, err)
	}
	if err := WriteAtomic(join, data, 0o644); err != nil {
		return fmt.Errorf("memory: write %q: %w", relPath, err)
	}
	return nil
}

// ReadFile reads relPath from the curated tree. Root-escaping paths are rejected.
func (c *Curated) ReadFile(relPath string) ([]byte, error) {
	clean, err := escapeClean(relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(c.root, clean))
	if err != nil {
		return nil, fmt.Errorf("memory: read %q: %w", relPath, err)
	}
	return data, nil
}

// WriteAbout writes the bot's self-description to about.md.
func (c *Curated) WriteAbout(text string) error {
	return c.WriteFile("about.md", []byte(text))
}

// ReadAbout returns about.md's text. An absent file is fine — it yields "" with no
// error, since a fresh pet has nothing written yet.
func (c *Curated) ReadAbout() (string, error) {
	data, err := c.ReadFile("about.md")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Directive returns the owner's DIRECTIVE.md constitution, READ-ONLY: core never
// writes it (disjoint writer sets — there is deliberately no DIRECTIVE writer). An
// absent directive yields "" with no error.
func (c *Curated) Directive() (string, error) {
	data, err := c.ReadFile("DIRECTIVE.md")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// AssembleContext builds the prompt context block in AD-7 retrieval order: the
// owner's directive first under an authoritative header, then the bot's about, then
// the recent conversation window oldest→newest. Empty sections are omitted; an
// all-empty input yields "". It returns a plain string only — core/memory must not
// import broker, so the recent window uses the local Message type. The LLM
// grep/FTS5 augmentation lands later; the worker calls this when the memory→worker
// integration arrives (Story 4.4).
func AssembleContext(directive, about string, recent []Message) string {
	var sections []string
	// Trim surrounding whitespace so a file's trailing newline (about.md/DIRECTIVE.md
	// almost always end in one) doesn't compound into blank lines between sections.
	if d := strings.TrimSpace(directive); d != "" {
		sections = append(sections, "### OWNER DIRECTIVE (authoritative)\n"+d)
	}
	if a := strings.TrimSpace(about); a != "" {
		sections = append(sections, "### ABOUT\n"+a)
	}
	if len(recent) > 0 {
		var b strings.Builder
		b.WriteString("### RECENT\n")
		for i, m := range recent {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(m.Role + ": " + m.Content)
		}
		sections = append(sections, b.String())
	}
	return strings.Join(sections, "\n\n")
}
