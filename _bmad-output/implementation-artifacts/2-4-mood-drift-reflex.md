---
baseline_commit: aaa1cd5
---

# Story 2.4: Mood-drift reflex

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want the pet's mood to drift over time on a cadence and persist,
so that its personality shifts believably across days without any LLM (FR2, FR4 reflex-driven, de-vibed AC3).

## Context

**Fourth story of Epic 2 (M1 — "The Soul").** The second resident reflex, and the one that finally makes the pet's mood *mean something visibly*. 2.1 gave it a `Mood` (valence) field and a `SetMood` method built **for this story** (see its doc comment — currently unused). 2.2 gave the face a render-agnostic `Expression`. 2.3 made the blink **expression-ready** (it pushes a `Face` from a local var, the seam left for 2.4). This story drifts `Mood` on a slow cadence, persists it, and **wires `Mood → Expression`** so the drifting mood is actually rendered on the face.

It is a **reflex-tier** behavior (AD-13): in-core, no worker, no LLM. Mirror the 2.3 blink: a thin supervised `Serve(ctx)` loop the reflex-tier scheduler (2.5) will later own as a job, verified under `testing/synctest`.

**Mood → Expression is in scope (flagged decision).** The literal ACs cover only drift + checkpoint, but the architecture (AD-6: core owns the face, mood drives it) and the explicit 2.2/2.3 deferrals route the `Mood → Expression` mapping here. Without it the drift is invisible and the feature is dead — so this story adds the mapping and makes the **blink** render the mood-derived expression. Mood-drift itself stays state-only (no compositor dependency); the expression-aware blink is what renders it (the blink already fires every few seconds while idle, so a mood change becomes visible within seconds). This keeps each reflex single-purpose: mood-drift owns state, blink owns rendering.

**This story does NOT:**
- build the reflex-tier scheduler (Story 2.5) — mood-drift runs its own supervised loop now; 2.5 registers it as a job with no rewrite
- add interaction-driven or mean-reverting mood dynamics — M1 drift is a fixed signed step per cadence, clamped (the de-vibed AC); richer curves are later
- make mood-drift push faces itself — rendering rides the expression-aware blink (no compositor dependency in mood-drift)
- change the compositor or renderer (2.2), or the contracts (the `Expression` set from 2.2 is sufficient)
- gate mood-drift on idle (unlike blink) — mood drifts on cadence regardless of interaction (AD: "over time on a cadence")

## Acceptance Criteria

1. **Drift by the configured step, checkpointed.**
   **Given** the mood-drift cadence (verifiable under `testing/synctest` with the fake clock advanced a simulated week)
   **When** the cadence elapses
   **Then** personality-state valence moves by the configured step and is checkpointed (AD-16).

2. **Accumulated drift is linear in the cadence count.**
   **Given** a simulated week of fake-clock advance
   **When** valence is asserted before and after
   **Then** the accumulated drift matches the configured per-cadence step times the number of elapsed cadences (within the valence clamp).

## Tasks / Subtasks

- [x] **Task 1 — Mood-drift reflex (`core/reflexes/mood.go`)** (AC: 1, 2)
  - [x] Created `core/reflexes/mood.go`: `MoodDrift` holding `*state.Store` (no compositor — rendering rides the blink); `NewMoodDrift(store)`.
  - [x] Tunable constants: `moodDriftInterval` 6h, `moodDriftStep` -0.02, clamp `moodValenceMin/Max` ±1.0 (also `moodHappyThreshold`/`moodSadThreshold` ±0.3 for the expression map).
  - [x] `Serve(ctx) error` — `time.NewTicker(moodDriftInterval)` loop: each tick `SetMood(clamp(Snapshot().Mood + moodDriftStep, min, max))` then `store.Checkpoint()` (log+continue on error); `ctx.Done()` → `ctx.Err()`.
  - [x] Unexported `clamp(v, lo, hi)` helper. Uses the existing `store.SetMood` (built in 2.1 for this story) — no `state` change; only mood-drift writes `Mood`, so snapshot→SetMood is race-free under the Store lock.

- [x] **Task 2 — Wire `Mood → Expression` into the blink (`core/reflexes/`)** (AC: 1)
  - [x] Added `expressionFor(mood) contracts.Expression` (in `mood.go`): `>= moodHappyThreshold` → Happy; `<= moodSadThreshold` → Sad; else Neutral.
  - [x] `Blink.blinkOnce` now reads `expressionFor(store.Snapshot().Mood)` and uses it for both the eyes-closed and eyes-open frames (replaced the 2.3 hardcoded-neutral seam; updated its doc comment).

- [x] **Task 3 — Wire into `cmd/shelldon/main.go`** (AC: 1)
  - [x] `mood := reflexes.NewMoodDrift(store)`; `root.Add(supervisor.Guard("reflex-mood", mood.Serve))` after `reflex-blink`. Updated the package doc comment.

- [x] **Task 4 — Tests (`testing/synctest`, stdlib, no testify)** (AC: 1, 2)
  - [x] `core/reflexes/mood_test.go`: `TestMoodDrift_AccumulatesAndCheckpoints` (synctest — a simulated week; asserts RAM mood == folded `clamp(step)` over `int(sleepDur/interval)` ticks AND `state.Load(path).Mood` matches, proving the checkpoint; before(0)≠after); `TestMoodDrift_ClampsValence` (a month of drift stays in `[min,max]`); `TestExpressionFor` (band → expression table).
  - [x] `core/reflexes/blink_test.go`: `TestBlinkOnce_RendersMoodExpression` (Mood 1.0 → blink frames carry `ExpressionHappy`).
  - [x] `go test -race ./...` passes (reflexes now 8 tests); native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues (`.golangci.yml` unchanged).

### Review Findings

- [x] [Review][Patch] Silent-pass blink test — drain loop exits on first `default` with zero assertions if channel is empty; no frame-count guard after the loop [core/reflexes/blink_test.go:TestBlinkOnce_RendersMoodExpression] — FIXED: added a `frames` counter and a post-loop `frames == 0` fail guard, so an empty channel fails instead of silently passing.
- [x] [Review][Patch] `TestMoodDrift_ClampsValence` starts at wrong boundary — initialises mood at `moodValenceMin` when `moodDriftStep < 0`, so the mood never moves and the clamp is never actually exercised; should start at `moodValenceMax` to exercise the full descent [core/reflexes/mood_test.go:TestMoodDrift_ClampsValence] — FIXED: now starts at the bound OPPOSITE the drift direction (max for a negative step), so the drift descends the full range and slams into the far clamp.
- [x] [Review][Defer] `MoodDrift.Serve` has no shutdown flush — a crash between `SetMood` and `Checkpoint()` in the same tick leaves RAM and disk diverged until the next 60s state-checkpoint fence; low risk given sequential call site and periodic fallback, but not a guaranteed AC-16 durability window [core/reflexes/mood.go:Serve] — deferred, pre-existing design choice (periodic checkpoint is the fence)

## Dev Notes

### Architecture constraints (binding)

- **AD-13 — Reflex cost tier.** "**reflex jobs** (mood drift, blink) run **in-core, no LLM, cheap CPU**." Mood-drift is named explicitly. No worker, no broker, no network. The reflex-tier scheduler (2.5) will own its cadence as a job with no core-loop refactor — so keep it a thin `Serve(ctx)`. [Source: ARCHITECTURE-SPINE.md#AD-13, epics.md#Story 2.5]
- **AD-16 — RAM working copy, checkpointed; core sole writer.** Mood lives in RAM (the `Store`); core is the single writer (AD-6) — mood-drift is the only writer of `Mood`. The drift is checkpointed to the one small file (the 2.1 `Checkpoint`), and RAM is never the source of truth for the (future) durable layers. [Source: ARCHITECTURE-SPINE.md#AD-16, AD-6]
- **AD-6 — Core owns the face; mood drives it via the compositor seam.** The face reflects mood through the region-compositor (2.2). This story makes the blink push the mood-derived `Expression`; the compositor/renderer are unchanged (the `Face.Expression` field from 2.2 carries it). [Source: ARCHITECTURE-SPINE.md#AD-6]
- **AD-10 — synctest for cadence.** A "simulated week" is exactly the `testing/synctest` fake-clock case. The drift uses a real `time.Ticker`; the bubble advances time deterministically, so accumulation (`step × ticks`) is exact. No clock interface. No randomness in mood-drift (deterministic step → the AC2 linear-accumulation assertion is exact). [Source: ARCHITECTURE-SPINE.md#AD-10]
- **NFR11 — SD-wear.** Mood drifts on a slow cadence (hours), so the extra checkpoint-on-drift is a handful of atomic writes per day — negligible, and it reuses the atomic `Checkpoint` (renameio). [Source: ARCHITECTURE-SPINE.md#NFR11]
- **NFR2 / NFR13 — pure-Go, offline.** No dependency; no network. arm64 `CGO_ENABLED=0` build stays green. [Source: ARCHITECTURE-SPINE.md#NFR2, NFR13]

### Key design decisions

- **Deterministic fixed step, clamped (de-vibed).** AC2 demands linear accumulation (`step × cadences`), so the drift is a fixed signed step per cadence — not a random walk. Clamp to `[-1, 1]` keeps valence bounded. Mean-reversion / interaction-driven mood is explicitly later.
- **Mood-drift is state-only; the blink renders it.** Keeping the compositor out of mood-drift makes each reflex single-purpose and avoids two writers racing to push faces. The expression-aware blink (Task 2) renders the current mood every few seconds while idle — so the drift is visible without mood-drift touching the display. (Trade-off: during an active conversation the blink is idle-gated off, so a fresh drift renders once the pet is idle again — fine at M1.)
- **Reuse `state.SetMood` (built in 2.1 for this story).** No new `state` method; mood-drift reads the snapshot, clamps, and `SetMood`s. Only mood-drift writes `Mood`, so this is race-free under the Store's existing lock — no lost-update risk.
- **`expressionFor` is core policy, not a contract.** The mood→expression thresholds are domain policy and live in `core/reflexes`, not in `contracts` (which holds data shapes, not policy). The `contracts.Expression` set (2.2) is the shared vocabulary.

### Previous story intelligence (Stories 2.1–2.3)

- **`state.SetMood(v float64)` exists and is currently unused** — added in 2.1 with the doc "the mood-drift reflex, Story 2.4". This story is its intended first caller (no dead code, no removal). [Source: core/state/state.go:62]
- **Blink left the expression seam open:** `blinkOnce` builds a local `Face` with `ExpressionNeutral` and a comment that 2.4 swaps in the mood-derived expression. Task 2 fulfills that. The existing blink tests still pass (mood 0 → neutral). [Source: core/reflexes/blink.go]
- **synctest cadence pattern (mirror blink/checkpoint):** start the loop goroutine, `time.Sleep` to fake-advance, `synctest.Wait()`, then cancel + `<-done` join before the bubble returns; construct `time.Now()`-dependent state inside the bubble. [Source: core/reflexes/blink_test.go, core/state/checkpoint_test.go]
- **Supervised-edge + `Checkpoint()`:** `store.Checkpoint()` writes the whole personality atomically to `store.path` (2.1); tests pass a `t.TempDir()` path. `Serve(ctx)` wrapped by `supervisor.Guard`. [Source: core/state/checkpoint.go, cmd/shelldon/main.go]
- **main start/drain order:** `state-checkpoint`, `core-dispatch`, `cli-transport`, `display-terminal`, `reflex-blink`. Add `reflex-mood` after `reflex-blink`. [Source: cmd/shelldon/main.go]
- **2.3 deferred finding (worth a cheap fix if you touch main):** the PCG rng is seeded `rand.NewPCG(seed, 0)` — second word fixed. Not in this story's scope; mood-drift uses no randomness. [Source: 2-3 Review Findings]
- **No new dependency** since 1.6. 2.4 adds none. [Source: go.mod]

### Project Structure Notes

- New: `core/reflexes/mood.go` (+ `mood_test.go`); optionally `core/reflexes/expression.go` for `expressionFor` (or keep it in `blink.go`).
- Modified: `core/reflexes/blink.go` (blink reads mood → expression), `cmd/shelldon/main.go` (construct `NewMoodDrift`, register `reflex-mood` edge). `core/state/state.go` is **not** modified — `SetMood` is reused as-is.
- `.golangci.yml` unchanged. No `go.mod`/`go.sum` change.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 2.4] — ACs; "personality shifts believably across days without any LLM"
- [Source: ...ARCHITECTURE-SPINE.md#AD-13] — reflex cost tier (mood drift named); one tier-shaped scheduler (2.5 owns cadence, no refactor)
- [Source: ...ARCHITECTURE-SPINE.md#AD-16] — RAM working copy, checkpointed; core sole writer
- [Source: ...ARCHITECTURE-SPINE.md#AD-6] — core owns the face; mood drives expression via the compositor seam
- [Source: ...ARCHITECTURE-SPINE.md#AD-10] — testing/synctest for cadence; deterministic accumulation
- [Source: core/state/state.go:62] — SetMood (built in 2.1 for this story)
- [Source: core/reflexes/blink.go] — blink's expression seam (Task 2 fulfills it); Serve(ctx) loop pattern to mirror
- [Source: core/reflexes/blink_test.go, core/state/checkpoint_test.go] — synctest patterns

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Opus 4.8, 1M context) via Claude Code dev-story workflow.

### Debug Log References

- TDD: `mood_test.go` + the blink-renders-mood test first (RED — `MoodDrift`/`expressionFor` undefined), then `mood.go` + the `blink.go` change to GREEN. 8 reflex tests pass under `-race`.
- synctest accumulation: the test computes `ticks = int(sleepDur/moodDriftInterval)` and folds `clamp(step)` that many times, so it's robust to changing the interval/step constants (no hardcoded 28). Float drift compared with `1e-9` epsilon.
- Checkpoint proof: the test reads `state.Load(path).Mood` after the week and matches it to the folded value — confirms AC1's "and is checkpointed", not just the RAM copy.
- Reused `state.SetMood` (defined-but-unused since 2.1, built for this story) — no `state.go` change. Only mood-drift writes `Mood`, so snapshot→SetMood is race-free under the Store lock (`-race` clean even with the blink concurrently reading `Mood`).

### Completion Notes List

- **AC1 satisfied (drift + checkpoint).** `MoodDrift.Serve` moves valence by `moodDriftStep` each `moodDriftInterval` (clamped) and calls `store.Checkpoint()` to persist it. `TestMoodDrift_AccumulatesAndCheckpoints` asserts both the RAM mood and the checkpoint file reflect the drift.
- **AC2 satisfied (linear accumulation).** Deterministic fixed step → after a simulated week the accumulated drift equals `step × ticks` (within the clamp). Asserted exactly under `testing/synctest`; before(0) ≠ after.
- **Mood → Expression wired (the 2.2/2.3 deferral, flagged).** `expressionFor` maps valence bands to `contracts.Expression`; the blink renders the current mood expression on every blink, so the drift is visible. `TestExpressionFor` + `TestBlinkOnce_RendersMoodExpression` cover it. The 2.3 blink tests still pass (mood 0 → neutral).
- **Clamp holds.** `TestMoodDrift_ClampsValence` drifts a simulated month and valence never leaves `[-1, 1]`.
- **Reflex-tier, offline (AD-13/NFR13).** No worker, no LLM, no network; state-only reflex; rendering rides the existing blink (no compositor dependency in mood-drift). Thin `Serve(ctx)` shaped for the Story 2.5 scheduler.
- **Scope held:** mood drift + checkpoint + the Mood→Expression rendering only. No interaction-driven/mean-reverting mood, no second face-pusher (mood-drift has no compositor dep), no scheduler (2.5), no contracts/compositor/renderer changes, no `state.go` change.
- **No new dependency**; deterministic (no rng). Native + arm64 `CGO_ENABLED=0` builds green.
- **Validation:** `go test -race -count=1 ./...` → all packages pass, no data race; `golangci-lint run` → 0 issues.

### File List

- `core/reflexes/mood.go` (new) — `MoodDrift` reflex (`NewMoodDrift`, `Serve`), `clamp`, `expressionFor`, mood-drift + expression constants
- `core/reflexes/mood_test.go` (new) — week-accumulation+checkpoint, clamp, expression-map tests (synctest)
- `core/reflexes/blink.go` (modified) — `blinkOnce` renders the mood-derived expression (replaced the 2.3 neutral seam)
- `core/reflexes/blink_test.go` (modified) — added `TestBlinkOnce_RendersMoodExpression`
- `cmd/shelldon/main.go` (modified) — construct `NewMoodDrift`, register supervised `reflex-mood` edge, doc comment
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (modified) — story status → in-progress → review

## Change Log

| Date       | Change                                                                 |
|------------|------------------------------------------------------------------------|
| 2026-06-21 | Added `core/reflexes.MoodDrift`, the second resident reflex: it drifts personality valence by a fixed clamped step on a slow cadence (6h) and checkpoints each drift, so the pet's mood shifts believably across days with no LLM (AD-13/AD-16). Verified under `testing/synctest` over a simulated week (linear accumulation = step × cadences) and a simulated month (clamp holds). Wired `Mood → Expression` (`expressionFor`) and made the blink render the current mood expression — the deferral promised by Stories 2.2/2.3 — so the drift is visible. Reuses `state.SetMood` (built in 2.1 for this story); no `state.go` change, no new dependency. Wired into `main` as the supervised `reflex-mood` edge. Native + arm64 builds green, `-race` suite passes, lint 0 issues (Story 2.4). |
| 2026-06-21 | Code review: 2 test-quality patches resolved (blink test now fails on zero frames instead of silent-passing; clamp test starts at the opposite bound so it actually exercises the clamp), 1 finding deferred. Gate re-run green. |
