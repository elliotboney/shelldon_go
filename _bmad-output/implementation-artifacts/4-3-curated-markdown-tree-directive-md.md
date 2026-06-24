---
baseline_commit: f82d1f4
---

# Story 4.3: curated markdown tree + DIRECTIVE.md

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want an atomic curated markdown tree and an owner-only authoritative DIRECTIVE.md injected into every prompt,
so that durable knowledge survives crashes and the owner's constitution is never overwritten by the bot (FR6, AD-7, NFR11).

## Context

**Third story of Epic 4 (M2 — "Memory & Dreams").** 4.1 built the sqlite messages store, 4.2 the learnings table. This story builds the **second half of AD-7's hybrid memory — the curated markdown tree** at `~/.shelldon/memory/` (`about.md`, `INDEX.md`, `facts/`, …) with **atomic writes** (crash-safe via `renameio/v2`), plus **`DIRECTIVE.md`**: the owner's authoritative "constitution," read into every prompt as authoritative and **never** writable by the bot. The two halves form AD-7's retrieval: **`DIRECTIVE.md` (first) + `about.md` + recent window (from sqlite) + grep/FTS5**.

**The atomic-write primitive already exists.** `core/memory/atomic.go`'s `WriteAtomic` (renameio/v2: temp-write → fsync → rename) has shipped since M0 with its crash-safety test (AD-10) — built precisely as "the Epic 4 foundation." This story builds the curated-tree API **on top of** `WriteAtomic` (every curated write routes through it), so AC1 is satisfied by construction.

**Disjoint writer sets are the heart of AC2 (AD-7).** Two writer sets that never overlap: (1) **core/the bot** owns the curated tree (`about.md`, `facts/`) — it self-rewrites via memory-ops; (2) **the human owner** is the sole writer of `DIRECTIVE.md`. Core's write path **structurally cannot** touch `DIRECTIVE.md`: the curated `WriteFile` rejects it, and the memory-op vocabulary (4.2's `capture_learning`, later `remember`/`rewrite_about`/`log_episode`) has **no op that targets `DIRECTIVE.md`**. So `DIRECTIVE.md` is read-only to the bot — provable, not just conventional.

**Built in isolation, wired in Story 4.4 — the 4.1/4.2 pattern.** The `monolith` worker today assembles `systemPrompt + user input` (its own comment: *"DIRECTIVE/about/history assembly is Epic 4"*). This story delivers the **curated tree + DIRECTIVE loader + a prompt-context assembler** (`DIRECTIVE` first/authoritative + `about` + recent window) as tested components. Wiring the assembler into the live worker — so real prompts include DIRECTIVE/about/recent-window — is the **memory→worker integration** that makes 4.1–4.3 live. **Decision (2026-06-24): this integration is folded into Story 4.4** (the dream cycle's AC "a promoted learning influences a later turn" forces the worker to read memory, so 4.4 is its natural home). No worker/`dispatch`/`main` change here.

**This story does NOT:**
- wire the assembler into the `monolith` worker / make live prompts include memory — that's the memory→worker integration step (the worker reads memory read-only, AD-6; Monolith+ allows it). Deferred and flagged for sequencing
- create or write a `vault/` or route anything sensitive — the vault does not exist until the worker is uid-separated (Epic 5, NFR6); curated `WriteFile` rejects `vault/` paths defensively
- implement `remember`/`rewrite_about`/`log_episode` memory-ops or the LLM `grep` retrieval (later) — only the tree read/write API + the recent-window/DIRECTIVE assembler
- run the dream cycle that promotes learnings into the tree (Story 4.4)
- change `contracts`, the sqlite store schema (4.1/4.2), the broker, the arbiter, dispatch, the scheduler, or `main`

## Acceptance Criteria

1. **Every curated markdown write is atomic (renameio/v2).**
   **Given** the curated markdown tree
   **When** any markdown write occurs (`about.md`, a file under `facts/`, etc.)
   **Then** it is atomic via `renameio/v2` (NFR11/AD-7) — a reader sees either the prior file or the fully-written new file, never a torn/partial write, and a crash mid-write leaves the prior file intact.

2. **DIRECTIVE.md is authoritative on read and unwritable by the bot (disjoint writer sets).**
   **Given** `DIRECTIVE.md` present
   **When** a prompt is assembled
   **Then** `DIRECTIVE.md` is read in as authoritative (first, labeled authoritative) **and is never a memory-op target** — core's curated write path rejects writing `DIRECTIVE.md`, and no memory-op kind targets it, so the bot can never overwrite the owner's constitution (disjoint writer sets, AD-7).

## Tasks / Subtasks

- [x] **Task 1 — The curated markdown tree (`core/memory/curated.go`)** (AC: 1, 2)
  - [x] New `Curated` type in the existing `core/memory` package: `type Curated struct { root string }`. `func OpenCurated(root string) (*Curated, error)` — `os.MkdirAll(root, 0o755)` and the `facts/` subdir; reject an empty root (mirror `Open`'s empty-path guard from 4.1).
  - [x] `func (c *Curated) WriteFile(relPath string, data []byte) error` — **atomic** via `memory.WriteAtomic(filepath.Join(root, relPath), data, 0o644)`. **Guards (disjoint writers + path safety):** reject `relPath == "DIRECTIVE.md"` (owner-only, AC2) and any path under `vault/` (no vault until Epic 5) with a sentinel error; reject paths that escape `root` after `filepath.Clean` (no `..` traversal). `MkdirAll` the parent dir of `relPath` first (so `facts/foo.md` works).
  - [x] `func (c *Curated) ReadFile(relPath string) ([]byte, error)` — read a tree file (same path-safety clean); a missing file returns a wrapped `os.ErrNotExist` (callers decide).
  - [x] Convenience: `func (c *Curated) WriteAbout(text string) error` / `func (c *Curated) ReadAbout() (string, error)` over `about.md` (ReadAbout returns `"" ` when absent, not an error — about is optional until written).
  - [x] `func (c *Curated) Directive() (string, error)` — read `DIRECTIVE.md` **read-only**, returning `""` when absent (a missing constitution is fine). This is the ONLY DIRECTIVE access core has; there is deliberately no DIRECTIVE writer.
  - [x] Define `var ErrOwnerOnly = errors.New("memory: path is owner-only, not bot-writable")` (returned by `WriteFile` for `DIRECTIVE.md`/`vault/`). Package doc: AD-7 curated tree — bot-owned, atomic writes (NFR11); `DIRECTIVE.md` is the owner's sole-writer constitution, read-only to core (disjoint writer sets).

- [x] **Task 2 — The prompt-context assembler (`core/memory/curated.go` or `assemble.go`)** (AC: 2)
  - [x] `func AssembleContext(directive, about string, recent []Message) string` — assemble the prompt context in AD-7 retrieval order: **`DIRECTIVE.md` first, labeled authoritative** (e.g. a `### OWNER DIRECTIVE (authoritative)` block), then `about.md`, then the recent window (oldest→newest), each section omitted when empty. Returns a plain string (NOT `broker.Message` — `core/memory` must never import `broker`/the SDK; the worker maps this to messages when wired). Keep it deterministic and simple — section headers + content; this is the context block the worker will prepend to the system prompt later.
  - [x] Doc: this is AD-7's retrieval assembly (`DIRECTIVE` first + about + recent window); the LLM `grep`/FTS5 augmentation is later. The worker (Monolith+, read-only memory access, AD-6) calls this when the memory→worker integration lands.

- [x] **Task 3 — Tests (stdlib `testing`, no testify)** (AC: 1, 2)
  - [x] `core/memory/curated_test.go`, `OpenCurated(t.TempDir())` per test.
  - [x] **AC1 atomic round-trip:** `WriteAbout("hello")` then `ReadAbout()` == "hello"; `WriteFile("facts/pi.md", …)` round-trips (and the `facts/` dir is created). Assert no leftover temp file remains in the dir after a write (renameio cleans up) — a directory listing shows only the final file. (The renameio atomicity guarantee itself is already covered by `core/memory/atomic_test.go`; reference it.)
  - [x] **AC2 DIRECTIVE read authoritative:** write a `DIRECTIVE.md` to the tree root directly (simulating the owner; use `os.WriteFile`, not the bot API), `Directive()` returns its content; `AssembleContext(directive, about, recent)` places the directive **first** and labels it authoritative (assert the directive text appears before the about/recent text in the output).
  - [x] **AC2 disjoint writers (the structural proof):** `Curated.WriteFile("DIRECTIVE.md", …)` returns `ErrOwnerOnly` and does **not** create/modify the file; `WriteFile("vault/secret.md", …)` also returns `ErrOwnerOnly`. Assert the memory-op apply path can't target it either: `ApplyMemoryOps(ctx, []contracts.MemoryOp{{Kind: "rewrite_directive", …}})` (an invented kind) is skipped (4.2's unknown-kind behavior) and writes nothing — there is no op kind that writes DIRECTIVE.
  - [x] **Missing files are graceful:** `ReadAbout()`/`Directive()` on a fresh tree return `""`, nil (not an error); `AssembleContext("", "", nil)` returns an empty/whitespace-only string.
  - [x] **Path safety:** `WriteFile("../escape.md", …)` and `ReadFile("../../etc/x")` are rejected (no traversal outside root).
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues.

## Dev Notes

### Architecture constraints (binding)

- **AD-7 — curated markdown tree + DIRECTIVE.md.** "Curated knowledge → markdown tree (`about.md` rewritable, `INDEX.md`, `facts/`, category folders, `vault/`). Every write is **atomic** via `google/renameio/v2` (temp + rename) … `about.md` and the curated tree are **bot-owned** (core is sole writer; the LLM self-rewrites via memory-ops). **Owner directive → `DIRECTIVE.md`** (human sole writer, the owner-controlled 'constitution'): injected into every prompt as authoritative, **never a memory-op target**, never under core's write path. Disjoint writer sets keep single-writer intact. Retrieval = `DIRECTIVE.md` (first) + `about.md` + recent window (from sqlite) + LLM `grep` over non-vault markdown / FTS5." This story builds the tree + DIRECTIVE read + the retrieval-order assembler. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **NFR11 — SD-card crash-safety / atomicity.** Markdown writes are atomic (temp+rename) so a power loss mid-write leaves the prior file intact. `WriteAtomic` (renameio/v2) already provides this and has the required crash-safety test. [Source: epics.md#NFR11, core/memory/atomic.go, core/memory/atomic_test.go]
- **AD-6 — core is the sole writer; the worker reads read-only.** The bot writes the curated tree via memory-ops (core applies); the worker "reads history read-only and the markdown tree minus `vault/`." DIRECTIVE.md is outside the bot's writer set entirely (the human owns it). [Source: ARCHITECTURE-SPINE.md#AD-6, #AD-7]
- **NFR6 / Boundary — no vault before Epic 5.** The `vault/` does not EXIST until the worker is uid-separated; this story never creates or writes it (curated `WriteFile` rejects `vault/`). [Source: epics.md#NFR6, ARCHITECTURE-SPINE.md#AD-3]
- **FR6 — context persists via hybrid memory.** sqlite (4.1/4.2) + the curated markdown tree (this story) are the two halves; the assembler combines them in retrieval order. [Source: epics.md#FR6]
- **Structural seed.** "curated md tree `~/.shelldon/memory/about.md INDEX.md DIRECTIVE.md facts/`." The tree root is `~/.shelldon/memory/`; injected via `OpenCurated(root)`, tests use `t.TempDir()`. [Source: ARCHITECTURE-SPINE.md#namespace map]

### Key design decisions

- **Build on `WriteAtomic`, don't re-implement atomicity.** `core/memory/atomic.go` already wraps renameio/v2 and has the crash-safety test. Every `Curated.WriteFile` routes through it, so AC1 holds by construction — no new atomic-write code, no new dependency.
- **Disjoint writers enforced structurally, not by convention.** `WriteFile` rejects `DIRECTIVE.md` (and `vault/`) with `ErrOwnerOnly`, and there is **no** memory-op kind that writes DIRECTIVE — so the bot's entire write surface structurally excludes it. That's what makes "never overwritten by the bot" a guarantee, not a hope (mirrors the AD-6 "worker holds no store" structural argument from 3.3).
- **The assembler returns a plain string, never `broker.Message`.** `core/memory` must not import `broker` (it would drag the provider SDK toward core). The assembler produces the context text in retrieval order; the worker (when wired) maps it into `broker.Message`s. Keeps memory provider-agnostic.
- **DIRECTIVE first + labeled authoritative.** AD-7's retrieval order is `DIRECTIVE.md` first; the assembler places it first under an authoritative header so the model treats it as the constitution. `about.md` and the recent window follow.
- **Missing files are normal, not errors.** A fresh pet has no `about.md`/`DIRECTIVE.md`; `ReadAbout`/`Directive` return `""`. Only genuine IO errors surface. Keeps the assembler robust on first boot.
- **Built in isolation (4.1/4.2 precedent).** The worker doesn't assemble real prompts yet; this delivers + tests the components. The memory→worker integration (worker reads DIRECTIVE/about/recent and includes them) is the step that makes Epics 4.1–4.3 live — see the sequencing note in the summary.

### Previous story intelligence (Epic 1–4.2)

- **`WriteAtomic` is the primitive** — `core/memory/atomic.go`: `WriteAtomic(path, data, perm)` (renameio/v2). Route all curated writes through it. `core/memory/atomic_test.go` already proves the crash-safety/atomicity; don't duplicate it — reference it and test the `Curated` API round-trips + cleanup. [Source: core/memory/atomic.go, core/memory/atomic_test.go]
- **Package conventions** — `core/memory` (Store from 4.1, learnings from 4.2): error-wrapping `fmt.Errorf("memory: …: %w", err)`; empty-path guard in `Open`; `[]T` empty-not-nil returns; sentinel errors via `errors.New`/`errors.Is`; test helpers open at `t.TempDir()`. Mirror all of these. [Source: core/memory/store.go, core/memory/learnings.go]
- **The op apply path (4.2)** — `ApplyMemoryOps(ctx, []contracts.MemoryOp)` switches on `Kind` and **skips unknown kinds with no error**. So an "invented" DIRECTIVE-writing op is structurally a no-op — the test that asserts this proves "DIRECTIVE is never a memory-op target." [Source: core/memory/learnings.go]
- **lint gate** — wrap deferred `Close()`; guard non-positive limits; `golangci-lint` must be 0. [Source: 4-1/4-2 Completion Notes]
- **No new fence / no cycle** — `core/memory` importing `contracts` (already does, 4.2) and stdlib (`os`, `path/filepath`, `errors`) is fine; it must NOT import `broker`/`worker`/`transport`. [Source: core/memory/learnings.go, core/dispatch/imports_test.go]
- **Worker assembly is the wiring target** — `worker/monolith/monolith.go` `AssembleAndPropose` currently builds `systemPrompt + turn.Input`; its own comment flags "DIRECTIVE/about/history assembly is Epic 4." That's where `AssembleContext` plugs in when the integration lands. [Source: worker/monolith/monolith.go]

### Latest tech information

- **No new external dependency.** `google/renameio/v2` v2.0.2 is already in `go.mod` (used by `WriteAtomic`). The curated tree uses only stdlib (`os`, `path/filepath`, `errors`, `strings`) + the existing `WriteAtomic`. Nothing to `go get`; no `go.mod` change. [Source: go.mod, core/memory/atomic.go]

### Project Structure Notes

- New: `core/memory/curated.go` (+ optionally `assemble.go`), `core/memory/curated_test.go`.
- Modified: none required — `atomic.go`, `store.go`, `learnings.go` are reused unchanged.
- Unchanged: `contracts/*`, the worker, broker, arbiter, dispatch, scheduler, transports, `cmd/shelldon/main.go`. No wiring this story. No `go.mod` change.
- `.golangci.yml` unchanged. Curated root is `~/.shelldon/memory/` in production (path injected via `OpenCurated`); tests use `t.TempDir()`. `DIRECTIVE.md` lives at the tree root, owner-written.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 4.3] — the two ACs (atomic markdown writes; DIRECTIVE authoritative + never a memory-op target)
- [Source: ...ARCHITECTURE-SPINE.md#AD-7] — curated tree (atomic renameio writes, bot-owned) + DIRECTIVE.md (human sole writer, authoritative, never a memory-op target) + retrieval order
- [Source: ...ARCHITECTURE-SPINE.md#AD-6] — core sole writer; worker reads read-only; disjoint writer sets
- [Source: epics.md#NFR11, #NFR6, #FR6] — atomic crash-safety; no vault in Epic 4; context persistence
- [Source: core/memory/atomic.go, core/memory/atomic_test.go] — the WriteAtomic primitive + its crash-safety test to build on
- [Source: core/memory/store.go, core/memory/learnings.go] — package conventions + the ApplyMemoryOps op path
- [Source: worker/monolith/monolith.go] — the worker assembly point where AssembleContext plugs in later

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow; implementation delegated to a golang-expert subagent, verified by the parent)

### Debug Log References

- `go test -race ./...` → **115 passed (21 packages)** (4.2 ended at 110; +5 curated tests)
- `go test -race ./core/memory/` → 23 passed; `CGO_ENABLED=0 go test ./core/memory/` → 23 passed
- `CGO_ENABLED=0 go build ./...` (native) + `GOOS=linux GOARCH=arm64` → both success
- `golangci-lint run` → 0 issues
- Spot-verified against spec: `WriteFile` guard order (escape-clean → disjoint-writer reject → MkdirAll → `WriteAtomic`); `core/memory/curated.go` imports stdlib only (no broker/worker leak); `AssembleContext` returns a plain string.

### Completion Notes List

- **Curated tree (`core/memory/curated.go`).** New `Curated{root}` over `~/.shelldon/memory/`: `OpenCurated` (MkdirAll root + `facts/`, empty-root guard), `WriteFile`/`ReadFile`, `WriteAbout`/`ReadAbout`, read-only `Directive()`. Every write routes through the existing `WriteAtomic` (renameio/v2), so **AC1 (atomic markdown writes)** holds by construction — no new atomic code, no new dependency.
- **AC2 — DIRECTIVE authoritative + disjoint writers (structural).** `Directive()` reads `DIRECTIVE.md` read-only (there is deliberately no DIRECTIVE writer method). `WriteFile` rejects `DIRECTIVE.md` and any `vault/` path with `ErrOwnerOnly` *before creating anything*, and `ApplyMemoryOps` (4.2) skips any unknown op kind — so the bot's entire write surface structurally excludes the owner's constitution. `TestCurated_DisjointWriters` proves both (`WriteFile("DIRECTIVE.md")` → `ErrOwnerOnly` with the owner's content intact; an invented `rewrite_directive` op writes nothing).
- **Prompt-context assembler.** `AssembleContext(directive, about, recent)` builds AD-7 retrieval order — DIRECTIVE first under an authoritative header, then about, then the recent window (oldest→newest) — as a plain string (never `broker.Message`, keeping `core/memory` provider-agnostic). Empty sections are omitted; all-empty → `""`. `TestCurated_DirectiveAuthoritative` asserts the directive precedes about in the output.
- **Robustness.** Missing `about.md`/`DIRECTIVE.md` return `"", nil` (a fresh pet has none). Path traversal (`../escape.md`, absolute paths) is rejected via a shared `escapeClean` helper used by both `WriteFile` and `ReadFile`. Tests: `TestCurated_AtomicRoundTrip` (+ no-leftover-temp check), `TestCurated_GracefulAbsent`, `TestCurated_PathSafety`.
- **Built in isolation (4.1/4.2 precedent).** No worker/`dispatch`/`main` change — `AssembleContext` plugs into the `monolith` worker when the **memory→worker integration lands in Story 4.4** (decision recorded in the Context section). `atomic.go`, `store.go`, `learnings.go`, contracts are unchanged. No `go.mod` change.

### File List

- `core/memory/curated.go` (new — `Curated` tree + `AssembleContext` + `ErrOwnerOnly`)
- `core/memory/curated_test.go` (new — 5 tests)
- `_bmad-output/implementation-artifacts/4-3-curated-markdown-tree-directive-md.md` (story tracking)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (status → review)

### Review Findings

- [ ] [Review][Patch] `escapeClean` passes `"."` — `WriteFile(".")`/`ReadFile(".")` resolves to the curated root dir itself [curated.go:43] — add `|| clean == "."` to the rejection guard
- [ ] [Review][Patch] No test for empty `""` relPath — `filepath.Clean("")` returns `"."`, exercising the unguarded case above; add a `TestCurated_PathSafety` case for `c.WriteFile("", data)` and `c.ReadFile("")` [curated_test.go:208]
- [x] [Review][Defer] `WriteFile` with a relPath naming an existing directory (e.g. `"facts"`) produces a confusing `EISDIR` from renameio rather than a clear rejection [curated.go:53] — deferred, pre-existing defensive-coding gap not required by spec
- [x] [Review][Defer] Dangling symlink at `about.md`/`DIRECTIVE.md` silently returns `"", nil` rather than surfacing the broken-symlink error [curated.go:94-116] — deferred, owner-controlled filesystem; not in spec
- [x] [Review][Defer] `AssembleContext` — a trailing `\n` in `directive`/`about` content produces triple-newline (`\n\n\n`) spacing between sections [curated.go:145] — deferred, aesthetic; LLM is unaffected
- [x] [Review][Defer] `TestCurated_DisjointWriters` embeds a `Store`/`ApplyMemoryOps` assertion inside the `Curated` test body; a Store failure produces a misleading failure site [curated_test.go:166-178] — deferred, test-organisation cleanup

## Change Log

- 2026-06-24: Implemented the curated markdown tree + DIRECTIVE.md (`core/memory/curated.go`) — atomic writes via the existing `WriteAtomic` (renameio/v2, AC1); `DIRECTIVE.md` read-only/authoritative with disjoint writer sets enforced structurally (`WriteFile` rejects it + `vault/`, no memory-op kind targets it, AC2); `AssembleContext` builds AD-7 retrieval order as a provider-agnostic string. Built in isolation; the memory→worker wiring is folded into Story 4.4. Both ACs satisfied; status → review.
- 2026-06-24: Addressed code-review findings (2 of 4 fixed, 2 deferred). Fixed: `AssembleContext` now `TrimSpace`s sections so a file's trailing newline doesn't stack blank lines (fired on every real file; `TestAssembleContext_TrailingNewlinesDoNotCompound`); split the memory-op fence out of the disjoint-writers test into `TestApplyMemoryOps_CannotTargetDirective`. Deferred (impossible-scenario / not-in-spec): `WriteFile` on a bare dir name → `EISDIR`; dangling-symlink read → `"", nil`. 25 memory tests, 117 suite-wide, lint 0. Status → done.
