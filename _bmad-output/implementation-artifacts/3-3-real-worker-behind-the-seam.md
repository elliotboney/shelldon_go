---
baseline_commit: ac11f997adf6bd1e9f1edb91c35ece57ffd337bc
---

# Story 3.3: Real worker behind the seam

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want a real LLM-backed worker behind the unchanged `Worker` seam that assembles prompts and proposes memory-ops,
so that owner messages get real conversational replies while core stays the sole writer (FR1, AD-2, AD-6, AD-11).

## Context

**Third story of Epic 3 (M1 — "The Brain"), and the integration story.** 3.1 built the credential boundary; 3.2 built the provider chain (`broker.Complete`). Both were tested in isolation with **no live caller**. This story builds the **real worker** that finally connects them: it implements the existing `Worker` seam (`AssembleAndPropose`, AD-2), assembles a prompt from the owner's message, calls `broker.Complete`, and returns the model's reply as a `contracts.Result`. Then it **wires the whole turn path together in `main.go`** — replacing `worker.Stub{}` in the arbiter with the real worker over a real broker. After this story, an owner message round-trips to a real LLM reply (with a key) or degrades to a reflex acknowledgement (without one).

**The seam stays unchanged (AD-2).** Core talks to the worker only through `worker.Worker` (the interface in `worker/worker.go`); `core/arbiter` imports that interface. The real worker is a **new implementation behind the seam** — the Monolith+ goroutine impl for M0–M2 (AD-2). Its isolation (goroutine + context timeout + `recover()`) is already provided by the arbiter's `Submit` (Story 2.6): the arbiter runs the worker in its own goroutine, bounds it with the turn timeout, recovers its panics, and fences a late `Result`. So the worker needs no new supervised edge — it is invoked through the arbiter.

**Critical layering constraint (the reason the worker gets its own package).** `core/arbiter` imports `worker` for the `Worker` interface. If the real implementation lived in `worker` and imported `broker` (which imports `go-openai` under `broker/internal/`), then `core/arbiter` would **transitively** pull a provider SDK into core's dependency graph — violating the spirit of AD-1 (core is LLM-free). So the real worker lives in a **separate package `worker/monolith`** that `core` never imports; only `main` imports it (and `broker`). `worker/monolith` satisfies `worker.Worker` **structurally** — it imports `contracts` + `broker`, not `worker`. The seam interface stays in `worker`; implementations are swappable edges (Monolith+ now, Privsep-lite at M3).

**Retro action item folded in (Epic 2 → 3.3):** the real worker **must honor `ctx` cancellation** — it threads the turn context into `broker.Complete`, so a turn timeout (2.6) or supersession aborts the in-flight LLM call (3.2 already propagates ctx into go-openai via `GetWithExecution`). This is AC2's "the in-flight LLM turn is killed."

**This story does NOT:**
- build memory / history / prompt assembly from stored context (Epic 4) — the prompt is a minimal system message + the owner's input; `Result.MemoryOps` stays empty (the worker proposes none yet, and **writes nothing** — AD-6 is satisfied structurally because the worker has no store reference)
- stream replies — `broker.Complete` is non-streaming (3.2); a streaming variant is deferred (the AC requires "an LLM reply is produced," not streaming)
- change the `Worker` interface, the arbiter, dispatch, or the broker — it adds a new `Worker` implementation and wires it in `main`
- add `turn_id` envelope minting — the AD-11 fence at M0 is the arbiter's `context` cancellation + dropped late `Result` (Story 2.6); full envelope-id fencing is later
- remove `worker.Stub` — it stays for the round-trip/unit tests (1.5 CLI e2e keeps using it)
- touch reflexes, scheduler, state, or contracts' shape

## Acceptance Criteria

1. **Real LLM reply; worker never writes memory.**
   **Given** the real worker behind the seam
   **When** an owner message is processed
   **Then** an LLM reply (from `broker.Complete`) is produced for that conversation (FR1), and any memory change is carried as a **proposed** `MemoryOp` in `Result` — the worker never writes state or memory directly (AD-6). (At M0 no memory exists, so `Result.MemoryOps` is empty and the worker holds no store — it structurally cannot write.)

2. **Cancelled turn is killed; late Result discarded.**
   **Given** an in-flight turn
   **When** the turn's `context` is cancelled (timeout/superseded)
   **Then** the worker's `broker.Complete` call is cancelled (the in-flight LLM call is killed), `AssembleAndPropose` returns the context error, and the arbiter discards any late `Result` for the closed turn (AD-11, the Story 2.6 fence).

## Tasks / Subtasks

- [x] **Task 1 — The Monolith+ real worker (`worker/monolith/monolith.go`)** (AC: 1, 2)
  - [x] New package `monolith` under `worker/`. Define the broker dependency as a **narrow interface** so the worker is testable without a real broker: `type Completer interface { Complete(ctx context.Context, req broker.Request) (broker.Response, error) }`. `*broker.Broker` satisfies it structurally.
  - [x] `type Worker struct { c Completer }` (holds **only** the completer — no store, no memory reference, so AD-6 "never writes" is structural). `New(c Completer) *Worker`.
  - [x] `AssembleAndPropose(ctx context.Context, turn contracts.Job) (contracts.Result, error)`: assemble a minimal prompt — a system message (`const systemPrompt`, a short pet persona; tunable story-time config) + a user message carrying `turn.Input`. Call `w.c.Complete(ctx, broker.Request{Messages: …})`, **threading ctx** (AC2). On success return `contracts.Result{Reply: resp.Text}` (`MemoryOps` empty — Epic 4). On error return `contracts.Result{}, err` (the broker's `ErrAllProvidersFailed` flows to the arbiter → dispatch → reflex ack, Story 2.6).
  - [x] This package imports `contracts` + `broker`, **not** `worker` — it satisfies `worker.Worker` structurally, so `core` never transitively imports `broker`/go-openai. Package doc: the Monolith+ impl of the AD-2 seam; isolation (goroutine + timeout + recover) comes from the arbiter (2.6).

- [x] **Task 2 — Wire the real worker + broker into `main.go`** (AC: 1, 2)
  - [x] Construct the broker once: `b := broker.New()` (logs credential presence/absence, AD-17). Construct the worker: `w := monolith.New(b)`. Replace `arbiter.New(worker.Stub{}, turnTimeout)` with `arbiter.New(w, turnTimeout)`.
  - [x] Remove the now-unused `worker` import from `main` if `worker.Stub` is no longer referenced there; add `broker` + `worker/monolith` imports. Update the `main` package doc to note the real worker + broker now back the turn path (a keyless broker degrades to reflex via the existing 2.6 path — the pet still runs offline).
  - [x] No new supervised edge — the worker runs inside the arbiter's `Submit` goroutine (2.6). No other wiring changes.

- [x] **Task 3 — Tests (stdlib, no testify)** (AC: 1, 2)
  - [x] **`worker/monolith/monolith_test.go` — AC1:** a fake `Completer` returns a known reply and **captures the request it received**; `AssembleAndPropose(ctx, contracts.Job{Input: "hello", ConvoID: "c1"})` → assert `Result.Reply` equals the fake's reply, the captured request's messages include the user input `"hello"`, and `Result.MemoryOps` is empty (worker proposed/wrote nothing). A compile-time/structural note: `Worker` holds only a `Completer` — no store — so it cannot write (AD-6).
  - [x] **AC1 (broker error → error out):** a fake `Completer` returning `broker.ErrAllProvidersFailed`; assert `AssembleAndPropose` returns that error (so the arbiter degrades to reflex, not a fake reply).
  - [x] **AC2 (cancellation propagates):** a fake `Completer` that blocks on `ctx.Done()` and returns `ctx.Err()`; run `AssembleAndPropose` in a goroutine, cancel the ctx, assert it returns a context error and that the completer observed the cancellation — proving the worker threads ctx so the in-flight call is killed. (The late-`Result`-discard half of AC2 is the arbiter fence, already proven by `TestArbiter_TimeoutClosesTurn`, Story 2.6 — reference it.)
  - [x] **Structural interface check:** assert `monolith.New(...)` satisfies `worker.Worker` (e.g. `var _ worker.Worker = (*monolith.Worker)(nil)` in the test, or a small assignment) so the seam wiring can't silently break.
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; `core/dispatch/imports_test.go` still passes (core stays free of transport/display — and core still does not import `worker/monolith`).

### Review Findings

- [x] [Review][Defer] Empty `turn.Input` forwarded to broker without guard [worker/monolith/monolith.go:AssembleAndPropose] — a blank input produces a wasted LLM call (or 4xx treated as non-transient ErrAllProvidersFailed); upstream arbiter/dispatch currently prevents this but no worker-level fence exists. Defer to Epic 4 when input assembly is formalized. — deferred, pre-existing
- [x] [Review][Defer] Empty `resp.Text` accepted as valid reply [worker/monolith/monolith.go:AssembleAndPropose] — a provider returning HTTP 200 with empty content propagates a blank reply to dispatch and the CLI. No current evidence this occurs, but no guard exists. Revisit when streaming/response validation is added. — deferred, pre-existing
- [x] [Review][Defer] `fakeCompleter.gotReq` written without synchronization [worker/monolith/monolith_test.go:fakeCompleter] — synchronous tests are safe today (`go test -race` passes), but the field is unprotected; any future test that reads `gotReq` after launching `AssembleAndPropose` in a goroutine will race. Add a mutex or restructure to return the captured request. — deferred, pre-existing
- [x] [Review][Defer] `blockUntilCancel` fake hangs forever if ctx is never canceled [worker/monolith/monolith_test.go:fakeCompleter.Complete] — `<-ctx.Done()` with no select-with-timeout; a future test that sets this flag and forgets to cancel will hang the test binary. — deferred, pre-existing
- [x] [Review][Defer] Cancellation test goroutine leaked on 2-second timeout failure path [worker/monolith/monolith_test.go:TestAssembleAndPropose_CancellationPropagates] — `done` channel is buffered so the goroutine exits eventually, but only after the test has already failed and teardown may be running. Low risk; clean up with `t.Cleanup` + cancel call. — deferred, pre-existing
- [x] [Review][Defer] No mechanical AD-1 import-graph guard — core's LLM-free constraint is enforced by convention (only `main` imports `worker/monolith`); no `go list -deps` check or depguard rule locks it. Pre-existing gap (Story 3.1 import fence only covers transport/display). Revisit alongside `core/dispatch/imports_test.go` pattern. — deferred, pre-existing

## Dev Notes

### Architecture constraints (binding)

- **AD-2 — Worker isolation seam; Monolith+ for M0–M2.** "the worker is a Go **interface** — `Worker.AssembleAndPropose(ctx, turn) (Result, error)`. Two implementations ship behind it: **Monolith+** (goroutine, `context` timeout + own `recover()`) for M0–M2 … ≤1 worker turn in flight (AD-8)." This story builds the Monolith+ implementation. Its goroutine + timeout + recover isolation is supplied by the arbiter's `Submit` (Story 2.6); the worker is a plain struct behind the seam. [Source: ARCHITECTURE-SPINE.md#AD-2, core/arbiter/arbiter.go]
- **AD-6 — Core is the sole writer; the worker only proposes.** "The worker **never** writes — a `Result` envelope carries *proposed* changes that core validates and applies; the worker reads history **read-only**." The worker holds no store/memory reference, so it structurally cannot write; `Result.MemoryOps` is the proposal channel (empty until Epic 4). [Source: ARCHITECTURE-SPINE.md#AD-6, contracts/result.go]
- **AD-11 — Turn identity & idempotent close.** "every turn carries a `turn_id` … core **fences** on it. A `Result` whose `turn_id` is already closed … is **discarded**. `turn_id` fencing is implemented via `context` cancellation." The worker threads `ctx` into `broker.Complete`; the arbiter's timeout cancels it and drops the late `Result` (2.6). Full envelope-id minting is later. [Source: ARCHITECTURE-SPINE.md#AD-11, 2-6 story]
- **AD-1 — Core is LLM-free, enforced by keeping provider deps out of core's import graph.** `core/arbiter` imports only the `worker` *interface*; the real implementation (which imports `broker`→go-openai) lives in `worker/monolith`, imported only by `main`. This keeps core free of the provider SDK transitively, not just directly. [Source: ARCHITECTURE-SPINE.md#AD-1, core/arbiter/arbiter.go]
- **AD-8 — A failed call degrades to reflex.** A broker error (chain exhausted, or no credential) returns from the worker as an error; the arbiter/dispatch turn it into a reflex acknowledgement (Story 2.6). The pet runs offline. [Source: ARCHITECTURE-SPINE.md#AD-8, core/dispatch/dispatch.go, 2-6 story]
- **FR1 — LLM response in the same conversation.** `turn.ConvoID` threads through dispatch to the `OutboundMessage` already (Story 1.5/2.6); the worker only produces the reply text. [Source: epics.md#Story 3.3, core/dispatch/dispatch.go]
- **Structural Seed — `worker/`.** "`worker/` # Worker interface (AssembleAndPropose): Monolith+ (goroutine) now, Privsep-lite (subprocess) @ M3." The Monolith+ impl is the new `worker/monolith` package. [Source: ARCHITECTURE-SPINE.md#Structural Seed]

### Key design decisions

- **Real worker in `worker/monolith`, not `worker`.** The hard constraint: `core/arbiter` imports `worker`. Putting the broker-importing implementation in `worker` would drag go-openai into core's transitive graph. A separate package keeps the seam interface in `worker` (core-safe) and the implementation as an edge `main` wires — exactly AD-2's "implementations behind the interface." Naming follows AD-2's "Monolith+".
- **Narrow `Completer` interface, not `*broker.Broker` directly.** The worker depends on a one-method `Completer` so tests inject a fake (no network, no real broker). `*broker.Broker` satisfies it structurally. This also keeps the worker agnostic to broker internals.
- **Worker holds no store — AD-6 by construction.** Rather than relying on review to confirm "the worker didn't write," the worker has no memory/state reference at all, so it *cannot*. `Result.MemoryOps` (empty now) is the only mutation channel, and core (Epic 4) is the writer.
- **ctx threading is the whole of AC2's "kill."** 3.2 made `broker.Complete` cancel the in-flight HTTP request on context cancellation; 2.6 made the arbiter cancel + fence. The worker's job is simply to pass `ctx` straight through — no swallowing, no `context.Background()`.
- **Minimal prompt now; assembly later.** A short system persona + the user message is enough for a real reply. DIRECTIVE/about/history assembly is Epic 4; streaming is deferred (broker is non-streaming).

### Previous story intelligence (Epic 1–3.2)

- **The seam + arbiter already provide isolation.** `arbiter.Submit` (2.6) runs the worker in a goroutine under `context.WithTimeout`, with a per-turn `defer recover()` (added in the 2.6 review) and a late-result fence. The worker is just a struct; don't re-add timeout/recover. [Source: core/arbiter/arbiter.go]
- **`broker.Complete(ctx, broker.Request) (broker.Response, error)`** is the egress; `broker.Request{Messages: []broker.Message{{Role, Content}}}`, `broker.Response{Text}`. A keyless broker returns `ErrAllProvidersFailed` at call time (no panic). [Source: broker/broker.go, broker/provider.go, 3-2 story]
- **`worker.Worker` interface + `worker.Stub`** — Stub echoes `turn.Input`; it stays for `transport/cli/cli_test.go` (1.5 e2e) and any unit test. The new worker is a sibling implementation. [Source: worker/worker.go, worker/stub.go]
- **dispatch already threads `ConvoID` and degrades on worker error** — `dispatch.Serve` submits `Job{Input, ConvoID}`, publishes `OutboundMessage{ConvoID, Reply}` on success, and a canned reflex ack on any non-shutdown error (2.6). No dispatch change needed. [Source: core/dispatch/dispatch.go]
- **main wiring pattern** — edges added as `supervisor.Guard(...)`; the arbiter is constructed inline (`arb := arbiter.New(worker.Stub{}, turnTimeout)`) and passed to `dispatch.New`. Swap the worker arg; the broker is a plain constructed dependency (not a supervised edge). [Source: cmd/shelldon/main.go]
- **Test-double pattern** — small in-test structs implementing the interface (`errWorker`/`hangingWorker` in dispatch, `fakeProvider` in broker). Mirror for the fake `Completer`. [Source: core/dispatch/dispatch_test.go, broker/broker_test.go]
- **`core/dispatch/imports_test.go`** guards core against `/transport` + `/display`; it does not check `/broker`, but the layering (core imports only the `worker` interface) keeps the SDK out of core regardless. Confirm `worker/monolith` is imported only by `main`. [Source: core/dispatch/imports_test.go]

### Project Structure Notes

- New: `worker/monolith/monolith.go`, `worker/monolith/monolith_test.go`.
- Modified: `cmd/shelldon/main.go` (construct broker + monolith worker, swap into the arbiter, adjust imports + package doc).
- Unchanged: `worker/worker.go` (the seam), `worker/stub.go` (kept), `core/*`, `broker/*`, `contracts/*`, reflexes, scheduler. No `go.mod` change (broker already pulls the SDKs).
- `.golangci.yml` unchanged. Verify `core` still does not import `broker`/`worker/monolith` (only `main` does).

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 3.3] — the two ACs (real reply + propose-not-write; cancel kills in-flight + late Result discarded)
- [Source: ...ARCHITECTURE-SPINE.md#AD-2] — Worker interface; Monolith+ goroutine impl for M0–M2; isolation via goroutine+timeout+recover
- [Source: ...ARCHITECTURE-SPINE.md#AD-6] — core sole writer; worker only proposes via Result.MemoryOps
- [Source: ...ARCHITECTURE-SPINE.md#AD-11, #AD-1, #AD-8] — ctx-cancellation fence; core LLM-free transitively; degrade-to-reflex
- [Source: worker/worker.go, worker/stub.go] — the unchanged seam + the kept stub
- [Source: core/arbiter/arbiter.go, 2-6 story] — the goroutine+timeout+recover+fence the worker runs inside
- [Source: broker/broker.go, broker/provider.go, 3-2 story] — `broker.Complete` egress the worker calls
- [Source: cmd/shelldon/main.go] — the wiring to swap (Stub → real worker over a real broker)
- [Source: _bmad-output/implementation-artifacts/epic-2-retro-2026-06-22.md] — "real worker MUST honor ctx cancellation" action item

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow)

### Debug Log References

- `go test -race ./...` → 74 passed in 18 packages
- `CGO_ENABLED=0 go build ./...` (native) → success
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` → success
- `golangci-lint run` → 0 issues
- Layering fence verified: no package under `core/` imports `broker` or `worker/monolith`; only `cmd/shelldon/main.go` imports `worker/monolith`.

### Completion Notes List

- **Monolith+ worker (`worker/monolith`).** New package satisfying `worker.Worker` structurally — it imports only `contracts` + `broker`, never `worker`, so `core` (which imports the `worker` interface) does not transitively pull go-openai into its graph (AD-1). The worker holds only a narrow one-method `Completer` (no store/memory), so AD-6 "never writes" is structural, not review-enforced; `Result.MemoryOps` stays empty until Epic 4.
- **AC1 — real reply / propose-not-write.** `AssembleAndPropose` assembles a minimal prompt (a `systemPrompt` pet persona + the owner's input as a user message), calls `broker.Complete`, and returns `Result{Reply: resp.Text}`. On broker failure the error (e.g. `ErrAllProvidersFailed`) flows out unwrapped so the arbiter/dispatch degrade to a reflex ack (AD-8, Story 2.6) rather than fabricating a reply.
- **AC2 — cancellation kills the in-flight call.** The worker threads the turn `ctx` straight into `broker.Complete` (no `context.Background()` swallow); 3.2 already cancels the in-flight HTTP request on ctx cancellation, and the arbiter (2.6) cancels + fences the late `Result` (`TestArbiter_TimeoutClosesTurn`). A blocking-fake test proves the completer observes the cancellation and `AssembleAndPropose` returns `context.Canceled`.
- **Wiring (`main.go`).** Constructed `b := broker.New()` (logs credential presence/absence, AD-17) and `w := monolith.New(b)`, swapped `arbiter.New(worker.Stub{}, …)` → `arbiter.New(w, …)`. Removed the now-unused `worker` import; added `broker` + `worker/monolith`. No new supervised edge — the worker runs inside the arbiter's `Submit` goroutine. A keyless broker still boots; the pet degrades to reflex and runs offline.
- **`worker.Stub` kept** (still used by `transport/cli/cli_test.go` and arbiter/dispatch tests). The interface, arbiter, dispatch, broker, and contracts are unchanged.

### File List

- `worker/monolith/monolith.go` (new)
- `worker/monolith/monolith_test.go` (new)
- `cmd/shelldon/main.go` (modified — construct broker + monolith worker, swap into arbiter, imports + package doc)
- `_bmad-output/implementation-artifacts/3-3-real-worker-behind-the-seam.md` (story tracking)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (status → review)

## Change Log

- 2026-06-22: Implemented the Monolith+ real worker behind the AD-2 seam (`worker/monolith`) and wired it over the broker into `main.go`, replacing `worker.Stub`. Owner messages now round-trip to a real LLM reply (with a key) or degrade to a reflex ack (without one). All ACs satisfied; status → review.
