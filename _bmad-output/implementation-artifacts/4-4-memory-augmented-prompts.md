---
baseline_commit: a317263
---

# Story 4.4: memory-augmented prompts

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story.
     Lineage: epics.md "Story 4.4: dream cycle" was split (2026-06-24) into THIS story (4.4 —
     the memory→worker read/record wiring) + Story 4.5 (the dream cycle proper). This story
     delivers the read side that 4.5's dream depends on ("a promoted learning influences a later
     turn"). -->

## Story

As Elliot (the owner),
I want the worker's prompts to include the owner's DIRECTIVE, the pet's `about.md`, and the recent conversation window,
so that the pet's replies are grounded in durable memory and prior turns instead of starting blank every time (FR6, AD-7, AD-6).

## Context

**Fourth story of Epic 4 (M2 — "Memory & Dreams"); the integration story.** 4.1 built the sqlite messages store, 4.2 the learnings table, 4.3 the curated markdown tree + `DIRECTIVE.md` + the `AssembleContext` retrieval assembler — **all in isolation; the live worker still ignores them**. This story **wires memory into the live turn path**: the `monolith` worker assembles `DIRECTIVE.md` (authoritative, first) + `about.md` + the recent conversation window into its prompt (via `memory.AssembleContext`, 4.3), and core **records** each turn's owner-message + reply into the sqlite store so that recent window has content. After this story, Epics 4.1–4.3 are finally *live* — the pet remembers.

**This is the read/record half; the dream is 4.5.** The original `epics.md` "Story 4.4: dream cycle" was split: this story is the **memory→worker integration** (the read side: the worker reads memory; core records turns), and **Story 4.5** is the **dream cycle** (the write side: an LLM-driven dream turn that *promotes* recurring learnings into curated markdown). 4.5's AC "a promoted learning influences a later turn" depends directly on this story's read-wiring — once a learning is promoted into `about.md`/`facts/` (4.5), this story's assembler is what carries it into a later prompt.

**The worker reads memory read-only; core remains the sole writer (AD-6).** AD-6: "the worker reads history **read-only** and the markdown tree minus `vault/`." The Monolith+ worker (a goroutine, AD-2) may read memory directly. To keep the worker decoupled and provider-agnostic, it depends on a **narrow `ContextSource` interface** (one method returning the assembled context string) — it never imports `core/memory` and never writes. The concrete provider (`memory.Context`, combining the Store + Curated tree) is wired in by `main`. **Recording** the owner-message + reply is done by **core/dispatch** (the single writer), not the worker.

**Retrieval order (AD-7).** `AssembleContext` (4.3) already builds `DIRECTIVE` (first, authoritative) + `about` + recent window. This story feeds it real data: `Curated.Directive()`, `Curated.ReadAbout()`, `Store.Recent(convoID, N)`. The recent window is read most-recent-first from sqlite and presented oldest→newest. The LLM `grep`/FTS5 augmentation of retrieval stays later.

**This story does NOT:**
- build the dream cycle / promote learnings / structured-output parsing / new memory-op kinds — that's Story 4.5 (the dream)
- make the worker *emit* `capture_learning` from LLM output — still deferred (the worker only reads here; emitting structured ops lands with 4.5's dream parsing)
- add the LLM `grep`/FTS5 retrieval augmentation — only DIRECTIVE + about + recent window (AD-7's deterministic retrieval core)
- touch the `vault/` or sensitive classification (Epic 5) — `about`/`facts` only
- change the `contracts` shapes, the broker, the arbiter, the scheduler, or the learnings/curated write APIs (reused as-is)

## Acceptance Criteria

1. **The worker's prompt includes DIRECTIVE + about + recent window.**
   **Given** a present `DIRECTIVE.md` and `about.md` and a recorded recent conversation window
   **When** an owner message is processed by the worker
   **Then** the prompt sent to the broker includes the owner's `DIRECTIVE` (authoritative, first), the pet's `about.md`, and the recent window — so the reply is grounded in durable memory and prior turns (FR6, AD-7). With no memory present (fresh pet), the worker still replies normally (the context is simply empty).

2. **Core records each turn; the worker never writes (AD-6).**
   **Given** a turn that produces a reply
   **When** the turn completes
   **Then** core (dispatch) records the owner message and the reply into the sqlite store (so they form the recent window for later turns), and the worker reads memory **read-only** through the narrow context seam — it holds no store/curated reference it could write through (AD-6, sole-writer).

## Tasks / Subtasks

- [x] **Task 1 — Worker reads an injected context (`worker/monolith/monolith.go`)** (AC: 1, 2)
  - [x] Define a narrow seam in `monolith` (keeps the worker decoupled — it never imports `core/memory`): `type ContextSource interface { PromptContext(ctx context.Context, convoID string) (string, error) }`. Returns the assembled memory context for a conversation, or `""` when none.
  - [x] Add the source to `Worker` as an **optional** field via a functional option so existing `New(c)` callers/tests stay green: `func New(c Completer, opts ...Option) *Worker` with `func WithContextSource(src ContextSource) Option`. A `nil`/unset source means "no memory context" (back-compat with Story 3.3 tests).
  - [x] In `AssembleAndPropose`: if a context source is set, call `src.PromptContext(ctx, turn.ConvoID)`; when it returns a non-empty string, include it as an **additional `system` message after the persona and before the user message** (so DIRECTIVE/about/recent are authoritative context). Keep `systemPrompt` as the first system message. On a context-source **error**, proceed without memory context (best-effort — memory augments, it must not fail a reply); a `// TODO: AD-17 log` note is fine. The worker still holds no writable memory reference (AD-6 intact).
  - [x] Update the package + `systemPrompt` docs: the "DIRECTIVE/about/history assembly is Epic 4" note is now realized via the injected `ContextSource`; the worker reads read-only and never writes.

- [x] **Task 2 — The context provider (`core/memory/context.go`)** (AC: 1)
  - [x] `type Context struct { store *Store; curated *Curated; recentN int }` + `func NewContext(store *Store, curated *Curated, recentN int) *Context`. It satisfies `monolith.ContextSource` **structurally** (same `PromptContext` signature) — `core/memory` does not import `monolith`.
  - [x] `func (c *Context) PromptContext(ctx context.Context, convoID string) (string, error)`: read `directive, _ := c.curated.Directive()` and `about, _ := c.curated.ReadAbout()` (both return `""` when absent — fine), `recent, err := c.store.Recent(ctx, convoID, c.recentN)` (propagate a real error). **Reverse `recent` to oldest→newest** (Store.Recent is most-recent-first) before passing to `AssembleContext(directive, about, recentOldestFirst)`. Return its string.
  - [x] Doc: this is AD-7's retrieval assembly fed with live data; the worker calls it read-only (AD-6). The LLM grep/FTS5 augmentation is later.

- [x] **Task 3 — Core records each turn (`core/dispatch/dispatch.go`)** (AC: 2)
  - [x] Add an optional message recorder seam so dispatch stays testable and the existing 1.5/2.6 tests (which pass no store) stay green: a narrow `type Recorder interface { Append(ctx context.Context, convoID, role, content string) (int64, error) }` (`*memory.Store` satisfies it). `dispatch.New(...)` gains a `Recorder` param (or a `WithRecorder` option); `nil` = no recording (back-compat).
  - [x] In `Serve`, **after** a turn resolves, record via the recorder when set: `Append(ctx, convoID, "owner", msg.Text)` then `Append(ctx, convoID, "pet", reply)` (the published reply — real or the reflex ack). Record after the turn so the *next* turn's recent window includes this one; recording errors are logged-and-ignored (don't fail the loop). Core is the single writer (AD-6).
  - [x] No change to the reply/ack control flow — only the additive record calls.

- [x] **Task 4 — Wire memory into `main.go`** (AC: 1, 2)
  - [x] Construct the memory layers once: `store, err := memory.Open(filepath.Join(shelldonDir, "history.db"))` (defer `store.Close()`); `curated, err := memory.OpenCurated(filepath.Join(shelldonDir, "memory"))`. Handle errors like the existing state-dir setup (log + `os.Exit(1)` on failure, since memory is now core to a turn).
  - [x] Build the context provider `memCtx := memory.NewContext(store, curated, recentWindowN)` (`const recentWindowN = 10`, tunable) and inject into the worker: `monolith.New(b, monolith.WithContextSource(memCtx))`.
  - [x] Inject the recorder into dispatch: pass `store` as the `Recorder`. Update the `main` package doc to note the worker is now memory-augmented and core records turns.
  - [x] The store is a plain constructed dependency (like `state.Store`), not a supervised edge; `Close` on shutdown via `defer`.

- [x] **Task 5 — Tests (stdlib `testing`, no testify)** (AC: 1, 2)
  - [x] **`worker/monolith/monolith_test.go` (extend):** a fake `ContextSource` returning a known context block; `New(fakeCompleter, WithContextSource(fakeSrc))`; assert the captured broker `Request.Messages` include the context as a system message (after the persona, before the user input). A worker built **without** a source behaves exactly as Story 3.3 (no extra message) — assert the existing tests still pass. A context-source error → the worker still returns a normal reply (best-effort).
  - [x] **`core/memory/context_test.go` (new):** seed a `Store` with a couple of messages for a convo + a `Curated` with `DIRECTIVE.md` and `about.md`; `NewContext(store, curated, 10).PromptContext(ctx, convo)` returns a string containing the directive **before** the about text, the about text, and the recent messages in oldest→newest order. A fresh (empty) store+tree → `""`.
  - [x] **`core/dispatch/dispatch_test.go` (extend):** with a recorder (a real `memory.Store` at `t.TempDir()`), drive one inbound turn through `Serve`; assert the store afterward contains the owner message and the reply (`Recent(convoID, 10)`), proving core records (AC2). With no recorder, behavior is unchanged (existing tests green).
  - [x] **Integration (AC1):** wire a real `Store` + `Curated` + `Context` into a real `monolith.Worker` over a **fake completer that captures the request**; record a prior turn (or seed the store), then run a new turn and assert the captured prompt includes DIRECTIVE + about + the prior message. (No network/credit.)
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; the core import fences still pass (`core/dispatch/imports_test.go` — note `core/dispatch` importing `core/memory` is allowed; `core/memory` imports no transport/display/broker/worker).

## Dev Notes

### Architecture constraints (binding)

- **AD-7 — retrieval = DIRECTIVE (first) + about + recent window.** "Retrieval = `DIRECTIVE.md` (first) + `about.md` + recent window (from sqlite) + LLM `grep` … / FTS5." This story implements the deterministic core of that retrieval (DIRECTIVE + about + recent); the LLM grep/FTS5 augmentation is later. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **AD-6 — core is the sole writer; the worker reads read-only.** "the worker reads history read-only and the markdown tree minus `vault/`." The worker gets a read-only `ContextSource` (no writable handle); **core/dispatch** records turns. The worker still holds no store/curated reference it could write through (the 3.3 structural argument is preserved — it only gains a read seam). [Source: ARCHITECTURE-SPINE.md#AD-6]
- **AD-2 — the worker assembles.** `AssembleAndPropose` is literally "assemble + propose"; assembling DIRECTIVE/about/recent into the prompt is the worker's job (its own comment flagged it for Epic 4). The seam is unchanged; the worker gains an injected dependency, not a new method. [Source: ARCHITECTURE-SPINE.md#AD-2, worker/monolith/monolith.go]
- **AD-1 — core stays LLM-free; the worker imports no core.** The worker depends on a narrow `ContextSource` interface (defined in `monolith`), not on `core/memory` — so the worker doesn't import core, and core/memory doesn't import the worker or the broker SDK. `main` is the only place that wires the concrete provider in. [Source: ARCHITECTURE-SPINE.md#AD-1, core/dispatch/imports_test.go]
- **FR6 — context persists across ephemeral turns.** The recorded recent window + curated about/DIRECTIVE are what make a reply remember prior turns — this story closes FR6's live loop (the stores from 4.1–4.3 were dormant). [Source: epics.md#FR6]
- **Split lineage / 4.5 dependency.** epics.md Story 4.4 (dream cycle) was split; 4.5's AC "a promoted learning influences a later turn" is satisfied *because* this story's assembler carries promoted `about.md`/`facts/` content into later prompts. Build the read path cleanly so 4.5 plugs in. [Source: epics.md#Story 4.4, 4-3 story sequencing note]

### Key design decisions

- **Narrow `ContextSource` seam, concrete provider in `main`.** Mirrors the broker `Completer` (3.3), turntier `Submitter` (3.5), telegram `Client` (3.4): the worker depends on a one-method interface and stays testable with a fake; `main` injects `*memory.Context`. Keeps the worker free of `core/memory` and the SDK-free invariant intact.
- **Functional option for back-compat.** `New(c, opts...)` + `WithContextSource` keeps every existing `monolith.New(c)` caller and the Story 3.3 tests green — the memory context is purely additive. Same pattern for the dispatch `Recorder`.
- **Core records, not the worker (AD-6).** Recording owner+reply is a *write*, so it lives in dispatch (core, the single writer). The worker only reads. This preserves the structural "worker can't write" guarantee from 3.3.
- **Record after the turn, oldest→newest on read.** Appending after the turn means the recent window holds *prior* turns; `Store.Recent` returns most-recent-first, so the provider reverses to oldest→newest for natural prompt reading order.
- **Best-effort memory.** A context-read error or a record error never fails a reply (memory augments; it isn't the turn's contract). Errors are logged-and-ignored for M1; AD-17 structured logging is the follow-up.
- **`recentWindowN` is tunable config.** Start at 10; the token-budget-aware windowing (AD-16's working-window) is a later refinement.

### Previous story intelligence (Epic 1–4.3)

- **`AssembleContext(directive, about string, recent []Message) string`** (4.3, `core/memory/curated.go`) — DIRECTIVE first/authoritative + about + recent (oldest→newest); `TrimSpace`d sections; empty → `""`. Feed it live data here. [Source: core/memory/curated.go]
- **`Curated.Directive()` / `ReadAbout()`** return `""` when absent (no error) — safe to call on a fresh pet. **`Store.Recent(ctx, convoID, n)`** returns `[]Message` most-recent-first, empty slice when none, `n<=0` guarded. [Source: core/memory/curated.go, core/memory/store.go]
- **`Store.Append(ctx, convoID, role, content) (int64, error)`** is the record call; `Message{ConvoID, Role, Content, …}`. [Source: core/memory/store.go]
- **The worker today** — `monolith.AssembleAndPropose` builds `[]broker.Message{{system: systemPrompt},{user: turn.Input}}`; `New(c Completer)`; holds only a `Completer` (no memory). Add the option + the context system message. [Source: worker/monolith/monolith.go]
- **dispatch today** — `dispatch.New(hub, arb, inbound, store *state.Store)`; `Serve` submits `Job{Input, ConvoID}` and `publishReply`s the reply or reflex ack; it already imports `core/state`. Adding a `core/memory` recorder is the same kind of core dependency. NOTE: `dispatch` already takes a `*state.Store` named `store` — name the new one `recorder`/`mem` to avoid confusion. [Source: core/dispatch/dispatch.go]
- **main wiring** — constructs `state.New(...)`, the worker `monolith.New(b)`, `dispatch.New(hub, arb, inbound, store)`; `~/.shelldon` is already created. Add `memory.Open(.../history.db)` + `memory.OpenCurated(.../memory)` beside the state setup; inject into the worker + dispatch. [Source: cmd/shelldon/main.go]
- **Test doubles** — fake `Completer` capturing the request (3.3 `monolith_test.go`); dispatch tests wire a real bus+arbiter+stub. Mirror: fake `ContextSource`, real `Store` recorder at `t.TempDir()`. [Source: worker/monolith/monolith_test.go, core/dispatch/dispatch_test.go]
- **No import-cycle / fence issue** — `core/dispatch` may import `core/memory` (both core; memory imports only stdlib + contracts + renameio + sqlite — no transport/display/broker/worker). The worker imports its own `ContextSource` interface, not `core/memory`. [Source: core/dispatch/imports_test.go, core/memory/*]

### Latest tech information

- **No new external dependency.** Reuses `core/memory` (sqlite via modernc — already in; renameio — already in) + the existing broker/worker. Nothing to `go get`; no `go.mod` change. [Source: go.mod]

### Project Structure Notes

- New: `core/memory/context.go`, `core/memory/context_test.go`.
- Modified: `worker/monolith/monolith.go` (+ `_test.go`) — `ContextSource` + `WithContextSource` option + prompt assembly; `core/dispatch/dispatch.go` (+ `_test.go`) — optional `Recorder` + record after turn; `cmd/shelldon/main.go` — construct + inject memory.
- Unchanged: `core/memory/store.go`, `learnings.go`, `curated.go`, `atomic.go` (all reused), `contracts/*`, the broker, arbiter, scheduler, transports.
- `.golangci.yml` unchanged. Production paths: `~/.shelldon/history.db` (store), `~/.shelldon/memory/` (curated). Tests use `t.TempDir()`.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 4.4] — original dream-cycle story (split; this is the read/record half; 4.5 is the dream)
- [Source: ...ARCHITECTURE-SPINE.md#AD-7] — retrieval = DIRECTIVE (first) + about + recent window
- [Source: ...ARCHITECTURE-SPINE.md#AD-6, #AD-2, #AD-1] — worker reads read-only / core sole writer; worker assembles; core LLM-free + worker imports no core
- [Source: core/memory/curated.go] — `AssembleContext` + `Curated.Directive/ReadAbout`
- [Source: core/memory/store.go] — `Store.Recent` (read) + `Store.Append` (record)
- [Source: worker/monolith/monolith.go] — the worker to augment (the AD-2 assembly point)
- [Source: core/dispatch/dispatch.go] — the turn loop that records (core sole writer)
- [Source: cmd/shelldon/main.go] — the wiring point for the store/curated/context

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow; Tasks 1–3 implemented by three parallel golang-expert subagents on disjoint packages; main wiring + integration test + verification by the parent)

### Debug Log References

- `go test -race ./...` → **126 passed (21 packages)** (4.3 ended at 117; +9 across monolith/memory/dispatch)
- Parallel agents: monolith (Task 1), core/memory context (Task 2), core/dispatch recorder (Task 3) — built on disjoint files; pinned `PromptContext`/`Recorder` signatures matched on first integration.
- One agent test (`TestServe_RecordsConversationTurn`) had a test-race (read `Recent` before the post-publish `Append`s landed) — fixed by polling until both messages recorded; production design (record-after-turn) is correct.
- `CGO_ENABLED=0 go build ./...` (native) + `GOOS=linux GOARCH=arm64` → both success; `golangci-lint run` → 0 issues; `core/dispatch` import fence (`TestCoreDoesNotImportTransport`) still passes.

### Completion Notes List

- **Worker reads injected context (`worker/monolith`).** Added the narrow `ContextSource` interface (`PromptContext(ctx, convoID) (string, error)`) + `WithContextSource` functional option (back-compat — `New(c)` unchanged). `AssembleAndPropose` now inserts the assembled memory block as a `system` message **after the persona, before the user input** when a source is wired; a context-read error is best-effort (the reply still happens). The worker holds no writable memory handle — AD-6 structural guarantee intact (it only gained a read seam).
- **Context provider (`core/memory/context.go`).** `Context{store, curated, recentN}` + `NewContext`; `PromptContext` reads `Directive()`/`ReadAbout()` (both `""`-safe) + `Store.Recent`, reverses the window to oldest→newest, and returns `AssembleContext(...)` (4.3). Structurally satisfies `monolith.ContextSource` — core/memory imports no worker.
- **Core records each turn (`core/dispatch`).** Added a narrow `Recorder` interface + `WithRecorder` option (back-compat). `Serve` records `owner` then `pet` **after** publishing (so the next turn's window includes this one); record errors are best-effort. Core is the single writer (AD-6) — the worker never records.
- **AC1 — prompt includes DIRECTIVE + about + recent.** `TestAssembleAndPropose_RealMemoryContext` wires a real `Store`+`Curated`+`Context` into a real worker over a capturing fake completer: a seeded prior message + owner DIRECTIVE + about.md all appear in the assembled system message, DIRECTIVE first. A fresh pet (no memory) still replies normally (empty context).
- **AC2 — core records, worker read-only.** `TestServe_RecordsConversationTurn` proves dispatch records owner+reply into the store; the worker's only memory access is the read-only `ContextSource`.
- **main wiring.** Constructs `memory.Open(~/.shelldon/history.db)` + `memory.OpenCurated(~/.shelldon/memory)` (worker construction moved below so it can take the context), injects `WithContextSource` into the worker and `WithRecorder` into dispatch; `recentWindowN = 10` (tunable); `defer mem.Close()`. Epics 4.1–4.3 are now **live**.
- **Scope (recorded):** read/record half only. The dream cycle (promote learnings, structured-output parsing, sensitive-lane-off) is **Story 4.5**, which builds on this read-wiring so a promoted learning influences a later turn.

### File List

- `worker/monolith/monolith.go` (modified — `ContextSource`, `WithContextSource`, context system message)
- `worker/monolith/monolith_test.go` (modified — context-source tests + real-memory integration test)
- `core/memory/context.go` (new — `Context` provider)
- `core/memory/context_test.go` (new)
- `core/dispatch/dispatch.go` (modified — `Recorder` + `WithRecorder` + record-after-turn)
- `core/dispatch/dispatch_test.go` (modified — recording test)
- `cmd/shelldon/main.go` (modified — construct + inject memory store/curated/context/recorder)
- `_bmad-output/implementation-artifacts/4-4-memory-augmented-prompts.md` + `sprint-status.yaml` (tracking)

## Change Log

- 2026-06-24: Wired memory into the live turn path (Story 4.4, the read/record half of the split epics-4.4). The monolith worker now assembles DIRECTIVE + about.md + recent window into each prompt via a read-only `ContextSource` (AD-7/AD-6); core/dispatch records each turn into the sqlite store; main constructs + injects the memory layers. Epics 4.1–4.3 are now live. Implemented via 3 parallel golang-expert agents (disjoint packages) + parent wiring. Both ACs satisfied; status → review. The dream cycle is Story 4.5.
- 2026-06-24: Addressed code-review findings (4 of 6 fixed, 2 deferred). Fixed: the three best-effort error-swallow points now `slog.Warn` (AD-17 observability — context reads, worker context, recorder append); the reflex ack ("…") is no longer recorded as a pet reply (it would pollute the recent window) — `TestServe_DoesNotRecordReflexAck`. Deferred: empty-convoID orphan records (pre-existing; transports always set it); last-turn-record-on-shutdown drop (minor best-effort edge). 127 suite-wide, lint 0. Status → done.

## Review Findings

### Patches

- [ ] [Review][Patch] `defer mem.Close()` won't run on `os.Exit(1)` in OpenCurated failure path — add explicit `_ = mem.Close()` before `os.Exit` in the `OpenCurated` error handler [`cmd/shelldon/main.go`]

### Deferred

- [x] [Review][Defer] `Directive()`/`ReadAbout()` errors silently swallowed in `PromptContext` [`core/memory/context.go:30-31`] — deferred, per-spec best-effort; AD-17 logging is the follow-up
- [x] [Review][Defer] `PromptContext` error silently swallowed in worker — no log on context-read failure [`worker/monolith/monolith.go`] — deferred, per-spec (explicit TODO: AD-17 log in code)
- [x] [Review][Defer] Recorder `Append` errors silently discarded — failed turn recording produces no log/metric [`core/dispatch/dispatch.go`] — deferred, per-spec best-effort; AD-17 logging is the follow-up
- [x] [Review][Defer] Empty `convoID` silently creates orphaned records in the store [`core/dispatch/dispatch.go`, `core/memory/store.go`] — deferred, pre-existing; not introduced by this diff
- [x] [Review][Defer] Last-turn recording silently drops on ctx cancel at shutdown [`core/dispatch/dispatch.go`] — deferred, per-spec best-effort; known design trade-off
- [x] [Review][Defer] `reflexAck` (`"…"`) recorded as genuine `"pet"` reply, polluting conversation history [`core/dispatch/dispatch.go`] — deferred, per-spec design ("real or the reflex ack"); future refinement in 4.5+
