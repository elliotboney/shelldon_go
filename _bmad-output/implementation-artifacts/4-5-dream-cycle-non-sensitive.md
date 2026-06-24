---
baseline_commit: 6ff261fc6d2ad33316a05142dc4c64ca3379df42
---
# Story 4.5: dream cycle (non-sensitive)

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story.
     Lineage: this is the dream-cycle half of the split epics.md "Story 4.4: dream cycle" (split
     2026-06-24 into 4.4 memory-wiring + 4.5 dream). Epic 4 finale. -->

## Story

As the system,
I want a scheduled dream turn that promotes recurring learnings into curated markdown, with the sensitive lane off,
so that durable learnings influence later turns while no vault-routing happens before Epic 5 (FR11, AD-15).

## Context

**Fifth story of Epic 4 (M2 — "Memory & Dreams"); the finale.** 4.2 gave the pet a `learnings` table (`capture_learning` → `pending` rows deduped by `pattern_key`). 4.4 made the worker memory-augmented (it reads DIRECTIVE + about + recent window into every prompt). This story closes the loop with the **dream cycle**: a scheduled, LLM-driven **worker turn** that reviews `pending` learnings, **promotes** the durable/recurring ones into the curated markdown tree (`facts/`/`about.md`), and **prunes** the rest — so a learning the pet keeps re-observing becomes durable knowledge that grounds future replies. The **sensitive-classification lane stays OFF** (no `vault/` exists until the worker is uid-separated, Epic 5).

**Dreaming reuses the machinery — it is a worker turn, not a new subsystem (AD-15).** AD-15: "dreaming is a scheduled introspective WORKER TURN … it **reuses the worker (via the AD-2 seam), broker, and arbiter** exactly like a normal turn." So the dream is a **turn-tier scheduled job** (3.5 `turntier`) whose `Build` produces a `dream`-kind `Job`, submitted **through the arbiter** (≤1 in flight, AD-8) to the **worker**; the worker dreams (LLM call) and **proposes** promote/prune memory-ops; core **applies** them via `OnResult` (3.6). This composes Stories 3.5 (turntier gating: cooldown + daily budget + battery) + 3.6 (`OnResult` apply hook) + 4.2 (learnings) + 4.3 (curated tree) + 4.4 (so a promoted learning shows up in a later prompt). No new "consolidation subsystem."

**The worker proposes; core is the single writer (AD-6).** The dream worker reads the pending learnings (handed to it in the dream `Job.Input` by the dream job — so the worker needs no new memory seam), asks the model which to promote/prune, and returns `Result.MemoryOps` (promote/prune ops). **Core** (the dream job's `OnResult`) applies them: marks the learning `promoted`/`pruned` in sqlite and appends the promoted observation into curated markdown — atomic writes (4.3), single-writer (AD-6). The worker never writes.

**"Influences a later turn" rides 4.4's read-wiring.** AC1's second half is satisfied *because* 4.4 made the worker read `about.md`/`facts/` into every prompt: once the dream promotes a learning into curated markdown, the very next normal turn's `AssembleContext` carries it into the prompt. This story proves that end-to-end.

**LIGHT scope (AD-15).** "no ERRORS/FEATURE_REQUESTS taxonomy, no skill promotion, no copy of v1's machinery." Promotion is by impact + recurrence; the model decides per-learning promote/prune; the curated target is `facts/`/`about.md` only. History consolidation/summarization (AD-15 item 1) is a later refinement — this story does the learnings promote/prune.

**This story does NOT:**
- create or write a `vault/` or classify anything sensitive — the sensitive lane is a **flag held OFF and tested off** (AC2); no vault exists until Epic 5 (NFR6/AD-3). Curated `WriteFile` already rejects `vault/` (4.3)
- summarize/compact conversation history (AD-15 consolidation item) — deferred; this story promotes/prunes learnings only
- make *normal* (reply) turns emit memory-ops — only the **dream** turn proposes ops (the reply-turn `capture_learning` emission is still deferred; the dream is the first turn that actually emits structured ops)
- change 4.4's read path, the broker, the scheduler loop, dispatch's reply path, or the curated/learnings read APIs (reused as-is)

## Acceptance Criteria

1. **A recurring pending learning is promoted and influences a later turn.**
   **Given** a recurring `pending` learning (recurrence above the promote threshold)
   **When** a dream turn runs
   **Then** the worker proposes promoting it, core applies the promotion (the learning is marked `promoted` in sqlite **and** its observation is written into curated markdown), and a **later normal turn's prompt includes it** (via 4.4's `AssembleContext`) — proving the promotion influenced a later turn (FR11/AD-15). Low-recurrence/low-value learnings are pruned (marked `pruned`), not promoted. (Proven with a fake completer returning canned dream decisions — no real credit.)

2. **The sensitive-classification lane is OFF and tested off.**
   **Given** the sensitive-classification lane
   **When** the dream cycle runs (no vault exists yet)
   **Then** the sensitive gating flag is **OFF** and asserted off — **nothing is routed to a `vault/`** (no vault path is ever written; the curated tree's `vault/` rejection holds). (Boundary's hole: the lane turns on only at Epic 5 once the worker is uid-separated.)

## Tasks / Subtasks

- [x] **Task 1 — Contract: dream turn-kind + promote/prune memory-ops (`contracts/job.go`, `contracts/result.go`)** (AC: 1) — additive only (NFR9/AD-10)
  - [x] `contracts/job.go`: add `Kind string` to `Job` (default `""` = a normal reply turn — back-compat) + `const (JobReply = "" ; JobDream = "dream")`. Doc: a `dream`-kind job tells the worker to run the introspective dream flow instead of replying.
  - [x] `contracts/result.go`: add `const MemoryOpPromoteLearning = "promote_learning"` and `const MemoryOpPruneLearning = "prune_learning"` (the dream's vocabulary; reuse the existing `PatternKey`/`Observation` fields — promote carries both, prune carries `PatternKey`). No struct change needed.
  - [x] Confirm the gob round-trip contract test (1.1) still passes (additive fields/consts).

- [x] **Task 2 — Core apply: promote/prune learnings (`core/memory/learnings.go` + a curated helper)** (AC: 1, 2)
  - [x] `func (s *Store) PromoteLearning(ctx, patternKey string) error` and `func (s *Store) PruneLearning(ctx, patternKey string) error` — update the learning's `status` to `'promoted'`/`'pruned'` (+ `updated_at`) by `pattern_key`. Add `const LearningStatusPromoted = "promoted"`, `const LearningStatusPruned = "pruned"`.
  - [x] A curated append for promoted knowledge: `func (c *Curated) AppendFact(relPath, text string) error` — read the existing file (`"" ` if absent), append `text` (newline-separated), `WriteFile` atomically (reuses 4.3's atomic write + the `DIRECTIVE.md`/`vault/` rejection). The dream promotes into `facts/learnings.md` (a `const`), never `vault/`.

- [x] **Task 3 — Worker dream behavior (`worker/monolith/monolith.go`)** (AC: 1, 2)
  - [x] Branch `AssembleAndPropose` on `turn.Kind`: `JobDream` → the dream flow; otherwise the existing reply flow (4.4) unchanged.
  - [x] Dream flow: system prompt = a **dream instruction** (`const dreamPrompt`: "You are dreaming. Review these candidate learnings and decide which to keep (promote) as durable knowledge and which to forget (prune). Respond ONLY with a JSON array …"); user message = `turn.Input` (the dream job formats the pending learnings into it). Call `w.c.Complete(ctx, …)`.
  - [x] **Parse structured output** into `[]contracts.MemoryOp`: robustly extract the JSON array from the response (strip ```json fences / surrounding prose; find the first `[`…`]`), `json.Unmarshal` into a small local `[]decision{ PatternKey, Action, Observation }`, map `promote`→`MemoryOp{Kind: MemoryOpPromoteLearning, PatternKey, Observation}`, `prune`→`MemoryOp{Kind: MemoryOpPruneLearning, PatternKey}`. On a parse failure return `Result{}` with **no** ops (a no-op dream, logged via slog — AD-17) rather than an error; the dream simply did nothing this cycle.
  - [x] Return `contracts.Result{MemoryOps: ops}` (no `Reply` — a dream produces no outbound message). The worker proposes only; it never writes (AD-6).
  - [x] Keep the SDK-free + structural guarantees: monolith still imports only `broker` + `contracts` + stdlib (`encoding/json`, `strings`, `log/slog`); no `core/memory` import.

- [x] **Task 4 — The dream job + apply (`core/dream/dream.go`)** (AC: 1, 2)
  - [x] New package `core/dream`. `func NewJob(arb turntier.Submitter, store *memory.Store, curated *memory.Curated, budget *turntier.Budget, power turntier.Power, cadence func() time.Duration, cooldown time.Duration) scheduler.Job` — builds a `turntier.Job` (3.5) and returns `.Scheduler()`.
  - [x] `Build`: read up to N `pending` learnings whose `RecurrenceCount >= promoteThreshold`-candidates (read `store.Learnings(ctx, LearningStatusPending, n)`); **format them into the dream `Job.Input`** (each line: `pattern_key | recurrence_count | observation`), return `contracts.Job{Kind: JobDream, Input: formatted}`. If there are **no** pending learnings, return a job whose later apply is a no-op (or skip — the gates already cap frequency; an empty dream is harmless).
  - [x] `OnResult(ctx, res, err)`: if `err != nil` return (a failed dream does nothing — AD-8). Else **core applies** `res.MemoryOps` (single writer, AD-6): for `MemoryOpPromoteLearning` → `store.PromoteLearning(patternKey)` **and** `curated.AppendFact(factsLearningsPath, observation)`; for `MemoryOpPruneLearning` → `store.PruneLearning(patternKey)`. **Sensitive lane:** a `const sensitiveLaneEnabled = false` — promotion **always** targets `facts/`/`about.md`, **never** `vault/` (and curated `WriteFile` rejects `vault/` regardless); add a guard/comment that the sensitive route is unreachable while the flag is off (Epic 5 turns it on).
  - [x] Package doc: AD-15 dreaming — a scheduled introspective worker turn (reuses worker/broker/arbiter via turntier+OnResult); the worker proposes promote/prune, core writes; sensitive lane OFF until Epic 5.

- [x] **Task 5 — Wire the dream job into `main.go`** (AC: 1, 2)
  - [x] Construct a dream budget (`turntier.NewBudget(dreamBudgetPerDay)`, conservative) and register `dream.NewJob(arb, mem, curated, dreamBudget, turntier.ACPower{}, dreamCadence, dreamCooldown)` into the existing scheduler alongside the reflex + proactive jobs (no scheduler-loop change — Yui's condition). Tunable consts: `dreamBudgetPerDay` (e.g. 2), `dreamCadence` (e.g. 6h consider), `dreamCooldown` (e.g. 12h). Update the `main` package doc.
  - [x] No other wiring — the worker already branches on `Job.Kind`; the dream job uses the same `arb`/`mem`/`curated` already constructed in 4.4.

- [x] **Task 6 — Tests (stdlib + testing/synctest, no testify)** (AC: 1, 2)
  - [x] **Worker dream parse (`worker/monolith`):** a fake completer returns a canned dream JSON (a promote + a prune decision, possibly fenced/with prose); `AssembleAndPropose(ctx, Job{Kind: JobDream, Input: "<learnings>"})` returns `Result.MemoryOps` = the mapped promote+prune ops (no `Reply`). A malformed/empty response → `Result{}` with no ops (no error). A reply-kind job is unchanged (4.4 behavior).
  - [x] **Apply (`core/memory`):** `PromoteLearning`/`PruneLearning` set the status; `AppendFact` appends atomically and accumulates (two appends → both lines present); `AppendFact("vault/x.md", …)`/`("DIRECTIVE.md", …)` is rejected (4.3 guard) — nothing sensitive written.
  - [x] **AC1 end-to-end (`core/dream`):** wire a real `Store` (seed a `pending` learning with high recurrence) + `Curated` + real `arbiter` over a **fake worker** that returns a promote op for that learning; build + run the dream job (via its scheduler.Job `Run`, or invoke Build→arb.Submit→OnResult directly) under synctest; assert afterward the learning is `promoted` in sqlite AND `facts/learnings.md` contains its observation. **Then** assemble a later normal-turn context (`memory.NewContext(store, curated, 10).PromptContext`) and assert it **includes the promoted observation** — proving "influences a later turn".
  - [x] **AC2 sensitive-off:** assert `dream.sensitiveLaneEnabled == false` (exported via an `export_test.go` if needed); after a full dream run, assert **no `vault/` directory/file exists** under the curated root (nothing routed to a vault).
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; the gob round-trip + reflex-tier fence + `core/dispatch` import fence still pass; `core/scheduler` stays unchanged (Yui's condition — the dream registers as a plain `scheduler.Job`).

## Dev Notes

### Architecture constraints (binding)

- **AD-15 — Dreaming & learning consolidation.** "dreaming is a scheduled introspective WORKER TURN … reuses the worker (via the AD-2 seam), broker, and arbiter … the worker (2) classifies `pending` `learnings` (AD-7) and **promotes** durable/high-value ones by impact + recurrence into curated markdown (`about.md`/`facts/`), routing sensitive ones to `vault/` (broker-gated, AD-9; only once the worker is across the process wall, AD-3), and (3) **prunes** the rest. The worker only **proposes** memory-ops; **core is the single writer** (AD-6). **LIGHT scope**." This story is the M2 dream (promote/prune learnings; sensitive lane OFF). [Source: ARCHITECTURE-SPINE.md#AD-15]
- **AD-13 / AD-8 — dreaming is a turn-tier job through the arbiter.** "turn jobs (reflection, **dreaming**, proactive pings) each cost a worker invocation + LLM, are few, cooldown-gated, and draw on the daily credit/turn BUDGET"; "Scheduler-proposed turn jobs go through the **arbiter**." The dream is a `turntier.Job` (3.5) submitted via the arbiter; `OnResult` (3.6) applies the proposal. [Source: ARCHITECTURE-SPINE.md#AD-13, #AD-8, core/turntier/turntier.go, core/proactive/proactive.go]
- **AD-6 — core is the sole writer; the worker only proposes.** The dream worker returns `Result.MemoryOps`; core (the dream job's `OnResult`) applies them to sqlite + curated markdown. The worker holds no store/curated handle (it gets the learnings via `Job.Input`). [Source: ARCHITECTURE-SPINE.md#AD-6]
- **AD-7 — promotion target is the curated markdown tree.** Promoted learnings go to `facts/`/`about.md` (atomic writes, 4.3); a later turn reads them via `AssembleContext` (4.4). [Source: ARCHITECTURE-SPINE.md#AD-7, core/memory/curated.go]
- **NFR6 / AD-3 — no vault before Epic 5.** The sensitive lane is a flag held OFF and tested off; nothing is routed to `vault/` (which doesn't exist; curated rejects it). The lane turns on at Epic 5 once the worker is uid-separated. [Source: epics.md#NFR6, ARCHITECTURE-SPINE.md#AD-3, #AD-15]
- **FR11 — light self-improving learning.** Capture (4.2) → dream promotes recurring ones (this story) → they influence later turns (via 4.4). [Source: epics.md#FR11]
- **Yui's condition (AD-13).** The dream registers as a plain `scheduler.Job` — the scheduler loop is unchanged (zero diff), as for the reflex + proactive jobs. [Source: core/scheduler/scheduler.go]

### Key design decisions

- **The dream job feeds learnings via `Job.Input`; the worker needs no memory seam.** `core/dream` reads `pending` learnings (it holds the `Store`) and formats them into the dream `Job.Input`; the worker just branches on `Job.Kind`, prompts, and parses. This keeps `monolith` decoupled (still imports only `broker` + `contracts` + stdlib — no `core/memory`), mirroring how 4.4 kept the read seam narrow.
- **Reuse turntier + OnResult (3.5/3.6), not a new subsystem (AD-15).** The dream is a `turntier.Job`: gated (cooldown/budget/battery), submitted through the arbiter (≤1 in flight), result applied in `OnResult`. Same shape as the proactive ping (3.6) — proven machinery. The scheduler loop is untouched.
- **Worker proposes ops; `core/dream` applies (AD-6).** Promote = `Store.PromoteLearning` (status) + `Curated.AppendFact` (durable markdown); prune = `Store.PruneLearning`. Both are core writes; the worker only returns the proposal.
- **Robust structured-output parsing, no-op on failure.** The model's dream response is parsed defensively (strip fences/prose, find the JSON array); a parse failure yields an empty `Result` (the dream did nothing) rather than an error — a malformed dream must never break the scheduler or corrupt memory. (LLM output is non-deterministic; tests use canned responses.)
- **Sensitive lane = a const flag OFF + the curated `vault/` rejection.** Two independent guarantees that nothing reaches a vault: the flag is `false` (no sensitive classification runs), and `Curated.WriteFile`/`AppendFact` reject `vault/` regardless (4.3). AC2 asserts both.
- **Promote-by-recurrence candidate filter, model decides.** `core/dream` only offers learnings above a recurrence threshold as candidates; the model picks promote vs prune among them. Keeps the dream cheap and the LIGHT scope (no elaborate taxonomy).

### Previous story intelligence (Epic 1–4.4)

- **turntier (3.5) + OnResult (3.6)** — `turntier.NewJob(turntier.Config{Name, Cadence, Cooldown, Build, Arbiter, Budget, Power, OnResult}).Scheduler()`; `Build func() contracts.Job`; `OnResult func(ctx, contracts.Result, error)` applies the result. `proactive.NewJob` (3.6) is the closest pattern to mirror (a turntier job whose OnResult does core work). [Source: core/turntier/turntier.go, core/proactive/proactive.go]
- **learnings (4.2)** — `Store.Learnings(ctx, status, n) ([]Learning, error)` (status filter, n>0 guarded), `Learning{PatternKey, Observation, RecurrenceCount, Status, …}`, `LearningStatusPending = "pending"`, `ApplyLearning` (capture). Add `PromoteLearning`/`PruneLearning` + the promoted/pruned status consts here. [Source: core/memory/learnings.go]
- **curated (4.3)** — `Curated.WriteFile` (atomic, rejects `DIRECTIVE.md`/`vault/`), `ReadFile`, `WriteAbout`/`ReadAbout`. Add `AppendFact` (read+append+WriteFile). Atomic writes via `WriteAtomic` (renameio). [Source: core/memory/curated.go]
- **memory→prompt (4.4)** — `AssembleContext` + `memory.Context.PromptContext` read `about.md`/`facts`? NOTE: 4.4's `PromptContext` reads `Directive()` + `ReadAbout()` + recent window — **it does not yet read `facts/`**. For "influences a later turn" via a `facts/learnings.md` promotion, EITHER promote into `about.md` (already read by `AssembleContext`) OR extend the context read to include `facts/learnings.md`. **Decision:** promote into a curated file the context already reads — append to `about.md` (or have the context provider also read `facts/learnings.md`); pick the simplest path that makes the promoted text appear in a later prompt, and cover it by the AC1 end-to-end test. [Source: core/memory/context.go, core/memory/curated.go]
- **worker (4.4)** — `monolith.AssembleAndPropose` branches will be added on `turn.Kind`; the reply flow (persona + injected context + user) stays as-is. The worker imports `broker` + `contracts` only; keep it that way (add `encoding/json`, `strings`, `log/slog` stdlib). [Source: worker/monolith/monolith.go]
- **main (4.4)** — `mem` (`*memory.Store`), `curated` (`*memory.Curated`), `arb` are already constructed; the scheduler already runs reflex + proactive jobs via `sched.Register(...)`. Register the dream job the same way. [Source: cmd/shelldon/main.go]
- **Test patterns** — fake completer capturing/returning canned responses (monolith), synctest for turntier timing, real Store/Curated at `t.TempDir()`, `core/proactive/proactive_test.go` for the turntier-job-with-OnResult test shape. [Source: worker/monolith/monolith_test.go, core/proactive/proactive_test.go, core/turntier/turntier_test.go]

### Latest tech information

- **No new external dependency.** Uses stdlib `encoding/json` (dream output parse) + the existing `core/turntier`, `core/memory`, `core/arbiter`, `contracts`, broker/worker. Nothing to `go get`; no `go.mod` change. [Source: go.mod]
- **Structured output (prompt design)** — the dream prompt asks for a strict JSON array of `{pattern_key, action: "promote"|"prune", observation}`; the parser strips markdown fences and locates the first balanced `[...]`. This is provider-agnostic (no function-calling dependency) and testable with canned strings. [Source: AD-15, worker/monolith]

### Project Structure Notes

- New: `core/dream/dream.go`, `core/dream/dream_test.go` (+ optional `export_test.go` for the sensitive-flag assertion).
- Modified: `contracts/job.go` (+`Kind`), `contracts/result.go` (+promote/prune consts), `core/memory/learnings.go` (+Promote/PruneLearning), `core/memory/curated.go` (+AppendFact), possibly `core/memory/context.go` (read `facts/learnings.md` if promoting there), `worker/monolith/monolith.go` (+`_test.go`) (dream branch), `cmd/shelldon/main.go` (register dream job).
- Unchanged: `core/scheduler/*` (zero diff — Yui's condition), the broker, arbiter, dispatch reply path, transports, reflexes.
- `.golangci.yml` unchanged. The dream writes only `facts/`/`about.md` under `~/.shelldon/memory/`; never `vault/`. Tests use `t.TempDir()`.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 4.4 (dream cycle)] — the two ACs (recurring learning promoted + influences a later turn; sensitive lane OFF, tested off)
- [Source: ...ARCHITECTURE-SPINE.md#AD-15] — dreaming = a scheduled worker turn reusing worker/broker/arbiter; promote by recurrence into curated markdown; prune the rest; worker proposes, core writes; LIGHT scope
- [Source: ...ARCHITECTURE-SPINE.md#AD-13, #AD-8, #AD-6, #AD-7] — turn-tier job through the arbiter; core sole writer; curated promotion target
- [Source: core/turntier/turntier.go, core/proactive/proactive.go] — the turntier job + OnResult apply pattern to mirror
- [Source: core/memory/learnings.go, core/memory/curated.go, core/memory/context.go] — learnings read/status + curated append + the 4.4 context read (for "influences a later turn")
- [Source: worker/monolith/monolith.go] — the worker to branch for the dream flow
- [Source: cmd/shelldon/main.go] — the scheduler wiring for the dream job

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (BMad dev-story workflow; Tasks 2 & 3 implemented in parallel by golang-expert subagents, Tasks 1/4/5/6 + integration by the orchestrator).

### Debug Log References

- golangci-lint surfaced 2 staticcheck issues in subagent-authored test files (QF1001 De Morgan in `context_test.go`, SA4017 dead empty-if in `learnings_test.go`); both rewritten into meaningful assertions. 0 issues after.

### Completion Notes List

- **Task 1 (contracts, additive):** `Job.Kind` + `JobReply`/`JobDream` consts; `MemoryOpPromoteLearning`/`MemoryOpPruneLearning` consts reusing `MemoryOp`'s existing fields. Gob round-trip contract test still passes.
- **Task 2 (core apply):** `Store.PromoteLearning`/`PruneLearning` (+`LearningStatusPromoted`/`Pruned`); `Curated.AppendFact` (atomic, accumulating, rejects `DIRECTIVE.md`/`vault/`); `Curated.ReadLearnings` + `FactsLearningsPath` const.
- **Key decision — "influences a later turn":** the dream promotes into `facts/learnings.md`, and `AssembleContext`/`PromptContext` were extended to read it as a new `### LEARNINGS` section (order: DIRECTIVE → ABOUT → LEARNINGS → RECENT). The worker seam (`PromptContext` signature) is unchanged — only its internal composition grew, so 4.4's read path/interface is intact. This was required because the Task 6 AC1 test asserts both that `facts/learnings.md` contains the observation **and** that a later prompt includes it; promoting into `about.md` would have failed the former.
- **Task 3 (worker dream):** `AssembleAndPropose` branches on `turn.Kind`; the dream flow prompts with `dreamPrompt`, calls the completer, and robustly parses a JSON array (`extractJSONArray` strips fences/prose) into `[]MemoryOp`. Parse failure → `Result{}` with no ops and no error (no-op dream, slog'd — AD-17). Monolith still imports only broker + contracts + stdlib.
- **Task 4 (`core/dream`):** turntier `Build` formats recurring (`recurrence ≥ promoteThreshold=2`) pending learnings into the `JobDream` input; `OnResult`/`applyResult` is core's single-writer apply (promote → `PromoteLearning` + `AppendFact`; prune → `PruneLearning`). `sensitiveLaneEnabled = false` (AC2): promotion always targets `facts/`, never `vault/`.
- **Task 5 (wiring):** `dream.NewJob(arb, mem, curated, dreamBudget, ACPower, dreamCadence, dreamCooldown)` registered alongside the proactive job (`dreamBudgetPerDay=2`, `dreamCadence=6h`, `dreamCooldown=12h`). `core/scheduler` is zero-diff (Yui's condition).
- **Task 6 (validation):** `go test -race ./...` → 145 pass / 22 packages; native + `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` builds succeed; `golangci-lint run` → 0 issues; gob round-trip + reflex/dispatch import fences pass; `core/scheduler` unchanged.
- **AC1 proven:** `core/dream` end-to-end test seeds a recurring learning, dreams over a real arbiter + fake worker, asserts it is `promoted` in sqlite AND written to `facts/learnings.md` AND present in a later `PromptContext`. **AC2 proven:** `sensitiveLaneEnabled == false` asserted, and no `vault/` path exists under the curated root after a full dream run.

### File List

- `core/dream/dream.go` (new)
- `core/dream/dream_test.go` (new)
- `contracts/job.go`
- `contracts/result.go`
- `core/memory/learnings.go`
- `core/memory/learnings_test.go`
- `core/memory/curated.go`
- `core/memory/curated_test.go`
- `core/memory/context.go`
- `core/memory/context_test.go`
- `worker/monolith/monolith.go`
- `worker/monolith/monolith_test.go`
- `cmd/shelldon/main.go`
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (tracking)

### Review Findings

- [x] [Review][Patch] PromoteLearning/PruneLearning no RowsAffected check — hallucinated pattern_key writes fact to curated markdown without a matching DB promotion [core/memory/learnings.go] — FIXED: `setLearningStatus` checks RowsAffected and returns new `ErrLearningNotFound` when no row matched; `applyResult` already `continue`s on promote error so the curated `AppendFact` is skipped. Covered by `TestPromotePruneUnknownKey` + `TestApplyResult_HallucinatedKeyWritesNoFact`.
- [x] [Review][Patch] AC1 e2e test calls applyResult directly, never exercises OnResult wiring from NewJob — a bug in turntier.Config.OnResult setup would not be caught [core/dream/dream_test.go] — FIXED: `TestDream_FullCycleThroughOnResult` drives the dream via `NewJob(...).Run(ctx)` (gates → Build → arbiter.Submit → OnResult → applyResult).
- [x] [Review][Patch] Gob round-trip test never encodes Kind="dream" (non-zero) — additive back-compat is asserted at zero value only [contracts/job.go] — FIXED: `TestEnvelopeRoundTrip` KindJob case now sets `Kind: JobDream` (non-zero survives the wire) and `TestAdditiveEvolution` asserts Kind zero-fills old→new.
- [x] [Review][Patch] Prune test seeds at recurrence=2 (equals promoteThreshold, not below it) and bypasses build() — AC1 prune path unproven through the full dream cycle [core/dream/dream_test.go] — FIXED: `TestDream_FullCycleThroughOnResult` proves the prune path through `build()` + the full dream cycle (worker prunes a candidate, learning ends `pruned`).
- [x] [Review][Defer] Non-atomic PromoteLearning + AppendFact — partial failure leaves learning promoted in sqlite but observation absent from curated markdown [core/dream/dream.go] — deferred, architectural; true atomicity requires a different approach
- [x] [Review][Defer] extractJSONArray bracket counting ignores string literals — unbalanced bracket in observation string causes graceful no-op dream (low severity, LLM-controlled) [worker/monolith/monolith.go] — deferred, low severity; consequence is graceful
- [x] [Review][Defer] Budget/cooldown in-memory only — resets on restart, can fire more than dreamBudgetPerDay times in crash-loop scenario [cmd/shelldon/main.go] — deferred, pre-existing turntier limitation, acknowledged in comments
- [x] [Review][Defer] Recurrence filter post-SQL not in query — above-threshold learnings past row 20 by updated_at order are invisible to the dream [core/dream/dream.go] — deferred, minor ordering tradeoff, acceptable at current volume
- [x] [Review][Defer] Duplicate JSON keys in model response → conflicting ops, last-write-wins — a promote+prune for same key leaves learning pruned [worker/monolith/monolith.go] — deferred, low probability with well-prompted LLM
- [x] [Review][Defer] Empty dream input when no candidates qualify — LLM call burns budget with no context (spec says harmless at 2/day cap) [core/dream/dream.go] — deferred, spec explicitly permits
- [x] [Review][Defer] AppendFact accepts arbitrary relPath — no canonical prefix guard for future callers beyond WriteFile's vault/ reject [core/memory/curated.go] — deferred, future caller concern, not a current bug
- [x] [Review][Defer] Race between PromoteLearning and concurrent ApplyLearning UPSERT — UPSERT resets status=pending, could undo a concurrent promotion [core/memory/learnings.go] — deferred, architectural; SQLite WAL + single-writer (AD-6) mitigates in practice
- [x] [Review][Defer] ApplyMemoryOps silently drops MemoryOpPromoteLearning/PruneLearning if routed through dispatch — no structural guard against wrong routing [core/memory/learnings.go] — deferred, no current code routes dream ops through dispatch

## Change Log

| Date       | Change |
|------------|--------|
| 2026-06-24 | Story 4.5 implemented: dream-cycle turn job (promote/prune learnings) reusing worker/broker/arbiter via turntier+OnResult; curated `facts/learnings.md` promotion read into prompt context; sensitive lane held OFF. All ACs satisfied; full validation green (145 tests -race, arm64 build, lint 0 issues). Status → review. |
| 2026-06-24 | Code review: 4 patches, 9 deferred, 8 dismissed. |
| 2026-06-24 | Review patches applied: ErrLearningNotFound guard against hallucinated keys (no orphan curated facts); full-cycle dream test through NewJob/OnResult wiring proving promote+prune via build(); gob round-trip now exercises non-zero Kind. +3 tests (148 -race), lint 0 issues, arm64 build green. Status → done. |
