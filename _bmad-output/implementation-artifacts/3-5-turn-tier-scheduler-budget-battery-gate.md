---
baseline_commit: ac11f997adf6bd1e9f1edb91c35ece57ffd337bc
---

# Story 3.5: Turn-tier scheduler + budget/battery gate

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the system,
I want turn-tier jobs (proactive, dreaming) that are cooldown-gated and bounded by a daily credit/turn budget, added to the existing scheduler,
so that background LLM spend is bounded and the turn tier is added without refactoring the reflex-tier loop (FR10 turn-tier, NFR14, AD-8, AD-13).

## Context

**Fifth story of Epic 3 (M1 — "The Brain").** Epic 2's Story 2.5 built the **reflex-tier scheduler**: a tier-shaped loop that runs named jobs at independent cadences, in-core, with no LLM (blink, mood-drift). This story adds the **turn tier** — jobs that cost a worker invocation + LLM call (proactive pings in 3.6, dreaming in Epic 4) — onto that *same* scheduler, **without touching the reflex-tier loop** (Yui's condition, AD-13). The turn tier is pure spend-control machinery: a **daily credit/turn budget**, a **per-job cooldown**, and a **battery-aware gate**, all applied *inside a turn job's `Run`* so the scheduler loop stays a dumb `{name, next-delay, run}` runner.

**The scheduler was built for this — by design it needs no change.** `core/scheduler`'s own doc already says: *"the turn tier (Story 3.5) layers on by registering more jobs with no change to this loop; turn jobs encode their arbiter/budget gating inside their own Run. The scheduler knows only {name, next-delay, run}."* So the turn tier is a **new package** that produces `scheduler.Job` values; the reflex scheduler package is **edited zero lines** — the strongest possible proof of "the reflex-tier loop is not refactored."

**Why a new package (`core/turntier`), not a file in `core/scheduler`.** The reflex-tier import fence (`core/scheduler/imports_test.go`, `TestReflexTierIsLLMFree`) **fails the build if `core/scheduler` or `core/reflexes` imports `/worker` or `/broker`** — reflex jobs must never reach the LLM path. A turn job *must* reach the worker (via the arbiter). So the turn-tier machinery lives in `core/turntier`, which imports `core/arbiter` + `contracts` + `core/scheduler` (for the `Job` shape) and is **outside** the fenced trees. The reflex tier stays LLM-free; the turn tier is allowed to spend.

**The gate order is the whole story.** A turn job's `Run` checks **cooldown → battery → budget**, and only if all three pass does it call `arbiter.Submit` (which enforces ≤1-worker-in-flight, AD-8). If any gate blocks, it returns **without invoking the worker** — that is AC1's "deferred/skipped and no worker invocation occurs." The arbiter (Story 1.3/2.6) is the ≤1-in-flight seam the turn submits through; the scheduler never invokes the worker directly (AD-13).

**This story does NOT:**
- build the actual proactive-ping behavior (Story 3.6) or dreaming (Epic 4) — it builds the **gating machinery + a generic turn-job** they will use; the real turn jobs and their `main` wiring arrive with 3.6/Epic 4
- change `core/scheduler`, `core/reflexes`, the arbiter, dispatch, the worker, or the broker — it adds a new `core/turntier` package only
- read real PiSugar2 battery state — the battery gate is a **seam** (`Power` interface) with a default "AC power, always allow" impl; the real reader is the PiSugar2 plugin at Epic 6 (Story 6.3). The seam exists now so the turn tier is battery-aware by construction
- add arbiter per-class coalescing / a pending catch-up slot — not required by either AC; the scheduler's cadence *is* the catch-up (a job that can't fire this tick re-evaluates next tick), and AD-8's per-class pending slot is deferred (the arbiter doc already defers it)
- mint envelope/turn ids or wire a second supervised edge — turn jobs run inside the existing reflex-scheduler edge's goroutines (2.5)

## Acceptance Criteria

1. **Budget/cooldown gate blocks the turn — no worker invocation.**
   **Given** turn-tier jobs registered alongside the reflex tier (tested with a fake clock + fake provider so no real credit burns)
   **When** a turn job is due but the daily credit/turn budget is exhausted or its cooldown has not elapsed
   **Then** the job is deferred/skipped and **no worker invocation occurs** (NFR14/AD-8) — `arbiter.Submit` is never called, so the worker's `AssembleAndPropose` is never reached.

2. **Turn tier added without refactoring the reflex loop.**
   **Given** the existing reflex-tier scheduler from Epic 2
   **When** the turn tier is added
   **Then** the reflex-tier loop is not refactored — the turn tier is layered onto the same tier-shaped scheduler (Yui's condition, AD-13). Concretely: `core/scheduler/scheduler.go` is unchanged, the turn tier registers as ordinary `scheduler.Job` values, and `TestReflexTierIsLLMFree` still passes (the reflex tier stays LLM-free; the turn tier carries the LLM path in its own package).

## Tasks / Subtasks

- [x] **Task 1 — The turn-tier package (`core/turntier/turntier.go`)** (AC: 1, 2)
  - [x] New package `turntier` under `core/`. It imports `contracts`, `core/arbiter` (the ≤1-in-flight submit seam) — actually a **narrow `Submitter` interface** so tests inject a fake — `core/scheduler` (for the `Job` shape), and stdlib. It is **outside** the reflex-tier fence trees, so reaching the worker path is allowed.
  - [x] **`Submitter` seam** (narrow, testable): `type Submitter interface { Submit(ctx context.Context, turn contracts.Job) (contracts.Result, error) }`. `*arbiter.Arbiter` satisfies it structurally. Tests inject a fake / a real arbiter over a fake worker.
  - [x] **`Power` battery gate seam:** `type Power interface { AllowsTurn() bool }` plus a default `type ACPower struct{}` whose `AllowsTurn()` returns `true`. Doc: the PiSugar2 plugin (Epic 6, Story 6.3) supplies a real reader that throttles on battery/low charge (NFR14/AD-13); until then the pet is treated as plugged in. A `nil` Power passed to a job defaults to `ACPower{}`.
  - [x] **`Budget`** — the shared daily credit/turn budget (NFR14/AD-8): `NewBudget(perDay int) *Budget`; an unexported `tryConsume(now time.Time) bool` that resets `used` to 0 at a calendar-day boundary (key on `now.Year()*1000 + now.YearDay()`), returns `false` when `used >= perDay`, else increments and returns `true`. Mutex-guarded (one `Budget` is shared across all turn jobs). Inject `now` (don't call `time.Now()` inside) so synctest drives it deterministically — OR call `time.Now()` (synctest controls it); prefer passing `now` from the job's `Run` for one clock source.
  - [x] **`Job`** — a turn-tier scheduled unit built from a config struct: `Name string`, `Cadence func() time.Duration` (how often to *consider* firing), `Cooldown time.Duration` (min interval between actual fires), `Build func() contracts.Job` (builds the turn input — proactive/dream specifics are 3.6/Epic 4), plus injected `Arbiter Submitter`, `Budget *Budget`, `Power Power`. Provide `NewJob(cfg Config) *Job` and `(*Job) Scheduler() scheduler.Job` returning `scheduler.Job{Name, NextDelay: cfg.Cadence, Run: j.run}`.
  - [x] **`run(ctx)` gate order = cooldown → battery → budget → submit.** With `now := time.Now()`: if `!lastFired.IsZero() && now.Sub(lastFired) < cooldown` → return (skip). If `!power.AllowsTurn()` → return (skip). If `!budget.tryConsume(now)` → return (skip). Only then set `lastFired = now` and call `arb.Submit(ctx, build())`. Every skip path returns **before** `Submit`, so no worker invocation (AC1). Guard `lastFired` with a mutex (the scheduler runs each job in its own goroutine; though one job = one goroutine, keep it race-clean). The `Submit` result/err is intentionally discarded here (the reply path is dispatch's; a proactive turn's outbound is 3.6) — a `// nolint`-free `_, _ =` with a short comment.
  - [x] Package doc: the Monolith+ turn tier (AD-13) — gating (cooldown/budget/battery) lives in the job's Run; the scheduler loop is untouched (Yui's condition); turns go through the arbiter (≤1 in flight, AD-8), never invoking the worker directly.

- [x] **Task 2 — Tests (stdlib + testing/synctest, no testify)** (AC: 1, 2)
  - [x] **`core/turntier/turntier_test.go`**, mirroring `core/scheduler/scheduler_test.go`'s `synctest.Test` + fake-clock pattern and `core/arbiter/arbiter_test.go`'s fake-worker pattern. Use a **fake worker** (records `AssembleAndPropose` call count via `atomic.Int32`) behind a **real `arbiter.New(fakeWorker, time.Minute)`**, so "no worker invocation" is proven at the worker, not just the seam.
  - [x] **AC1 (budget exhausted → no invocation):** `NewBudget(0)`; register one turn job (cadence 1s, cooldown 0) into a real `scheduler.New()`; run `scheduler.Serve` under synctest; advance several seconds; assert the fake worker's call count is **0** (budget gate blocked every fire, no worker invocation).
  - [x] **AC1 (cooldown not elapsed → skip):** budget large, cadence 1s, cooldown 10s; advance 5s; assert call count is **1** — the first fire passes, the next four are inside the cooldown and skipped.
  - [x] **AC1 (budget caps fires):** `NewBudget(2)`, cadence 1s, cooldown 0; advance 5s; assert call count is **2** — the budget caps invocations regardless of cadence.
  - [x] **Battery gate (NFR14):** a `lowPower` stub (`AllowsTurn()==false`); advance; assert call count is **0** — the battery gate blocks the turn (proves the seam works ahead of the Epic 6 reader).
  - [x] **Daily reset:** `NewBudget(1)`, cooldown 0; fire once (count 1); advance past a day boundary (e.g. 25h); assert it fires again (count 2) — the budget resets daily.
  - [x] **AC2 (Yui's condition):** the turn job registers as a plain `scheduler.Job` into a real `scheduler.New()` (the tests above already do this) and `core/scheduler/scheduler.go` is unchanged. Confirm `go test ./core/scheduler/` (incl. `TestReflexTierIsLLMFree`) still passes — the reflex tier stays LLM-free while the turn tier carries the worker path in `core/turntier`.
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; `core/scheduler` shows no diff (Yui's condition).

### Review Findings

- [ ] [Review][Patch] Nil Budget causes panic in run() — no nil guard in NewJob for Config.Budget [core/turntier/turntier.go:98]
- [x] [Review][Defer] TOCTOU window on lastFired check-and-update [core/turntier/turntier.go:120-136] — deferred, scheduler single-goroutine-per-job guarantee makes this non-exploitable in current usage
- [x] [Review][Defer] runGatedJob helper silently discards caller's Config fields other than Name [core/turntier/turntier_test.go:40-64] — deferred, pre-existing test design; no current test pre-populates other fields
- [x] [Review][Defer] Year-boundary (Dec 31→Jan 1) untested in budget reset unit test [core/turntier/turntier.go:62] — deferred, formula is mathematically correct but edge case has no test coverage

## Dev Notes

### Architecture constraints (binding)

- **AD-13 — Scheduler: the autonomous mind (multi-cadence, cost-tiered, battery-aware).** "a **core-resident scheduler** owns the pet's self-driven life as **named jobs**, each with its own **cadence** … Each job is tagged by **COST TIER**: **reflex jobs** … run **in-core, no LLM**; **turn jobs** (reflection, dreaming, proactive pings) each cost a worker invocation + LLM, are few, cooldown-gated, and draw on the daily credit/turn BUDGET (AD-8). **Battery-aware:** reads PiSugar2 power state, stretches cadences / skips non-essential LLM turns on battery … **Scheduler-proposed turn jobs go through the arbiter** (AD-8) — same ≤1-worker bound, coalescing, and gate; the scheduler never invokes the worker directly." This story implements the turn-tier cost layer. [Source: ARCHITECTURE-SPINE.md#AD-13]
- **AD-8 — The arbiter governs the brain: ≤1 turn, coalesce, cost/battery-gated.** "All turn-jobs (proactive pings + dreaming) carry a **cost** and are gated by a **daily credit/turn BUDGET** and **PiSugar2 battery-aware backoff** (read via the scheduler): skip/defer non-essential LLM turns on battery/low charge, livelier when plugged in." The budget + battery gate are this story; per-class coalescing/pending-slot is deferred (not in the ACs; the arbiter doc defers it too). [Source: ARCHITECTURE-SPINE.md#AD-8, core/arbiter/arbiter.go]
- **NFR14 — Battery + credit-aware autonomy.** "no unbounded background LLM spend; battery-aware backoff." The daily budget bounds spend; the `Power` seam enables battery backoff. [Source: epics.md#NFR14]
- **FR10 (turn-tier) — autonomous scheduler.** The reflex tier shipped in Epic 2; the turn tier ships here on the same scheduler. [Source: epics.md#FR Coverage Map, #Story 3.5]
- **Yui's condition (AD-13) — no reflex-loop refactor.** The reflex-tier loop is layered on, not rewritten. Proven by `core/scheduler/scheduler.go` being unchanged and the turn tier registering as ordinary `scheduler.Job` values. [Source: epics.md#Story 3.5, ARCHITECTURE-SPINE.md#AD-13, core/scheduler/scheduler.go]
- **Structural Seed — `core/`.** The turn tier is a core-resident component (the autonomous mind is in core). The new package is `core/turntier`. [Source: ARCHITECTURE-SPINE.md#Structural Seed]

### Key design decisions

- **Turn tier = a new `core/turntier` package, scheduler untouched.** The reflex-tier fence forbids `core/scheduler`/`core/reflexes` from importing `/worker` or `/broker`. A turn job must reach the worker (via the arbiter), so its machinery lives outside the fence in `core/turntier`. This *also* makes Yui's condition literal: `scheduler.go` gets zero edits.
- **Gating lives in the job's `Run`, not the scheduler and not the arbiter.** The scheduler stays `{name, next-delay, run}`; the arbiter stays the ≤1-in-flight gate. Cooldown/budget/battery are turn-tier policy, so they live in `turntier.Job.run`. This is exactly what `scheduler.go`'s doc already anticipated.
- **Gate order cooldown → battery → budget → submit.** Cheapest/most-common skips first; budget (the scarce shared resource) is consumed last and only when the turn will actually fire, so a cooldown/battery skip never burns budget. Every skip returns before `arbiter.Submit` → AC1's "no worker invocation."
- **Battery is a seam now, hardware later.** `Power` interface + `ACPower` default (always allow). The PiSugar2 plugin (Epic 6, Story 6.3) injects a real reader. This keeps M1 hardware-free while making the turn tier battery-aware by construction (NFR14) — matching the repo idiom "narrow interfaces over every external seam, wired by constructor injection."
- **Narrow `Submitter` seam over `*arbiter.Arbiter`.** The turn job depends on a one-method `Submitter` so tests inject a fake or a real arbiter over a fake worker — no network, no real credit (the AC's "fake clock + fake provider"). Mirrors broker's `Provider`/monolith's `Completer`/telegram's `Client`.
- **Budget keyed on the calendar day; clock via synctest.** `tryConsume(now)` resets at a day boundary. Passing `now` in (rather than calling `time.Now()` internally) keeps one clock source and is synctest-deterministic. The existing scheduler uses `time.Now()`/timers directly under synctest, so either works; prefer injecting `now`.
- **No `main` wiring yet.** No real turn job exists until 3.6 (proactive ping) / Epic 4 (dream). Wiring a placeholder job would be speculative. 3.5 ships the machinery + a generic `Job`; 3.6 constructs the shared `Budget`/`Power`, builds the proactive-ping `Job`, and registers it into the scheduler in `main`. AC2 is test-proven via a real `scheduler.New()`.

### Previous story intelligence (Epic 1–3.4)

- **The reflex scheduler is the host, unchanged** — `core/scheduler/scheduler.go`: `Job{Name, NextDelay func() time.Duration, Run func(context.Context)}`; `New()`, `Register(Job)`, `Serve(ctx)` runs each job in its own goroutine on its own cadence with a per-job `recover` (AD-5) and a `minDelay` floor (insurance for turn-tier cadences that compute "fire now"). Register turn jobs the same way reflexes are registered. [Source: core/scheduler/scheduler.go]
- **The arbiter is the ≤1-in-flight submit seam** — `arbiter.New(worker, timeout)`; `Submit(ctx, contracts.Job) (contracts.Result, error)`: admits one turn, rejects a concurrent one with `ErrTurnInFlight`, bounds each with a timeout, recovers worker panics. Turn jobs submit through it; a rejected turn is simply skipped (the cadence is the catch-up). [Source: core/arbiter/arbiter.go]
- **`contracts.Job{Input, ConvoID}`** is the turn input the worker consumes; `Build func() contracts.Job` produces it. A proactive turn's actual Input/ConvoID is 3.6's concern; 3.5's `Build` is injected. [Source: contracts/job.go, core/dispatch/dispatch.go]
- **Test patterns to mirror:** `core/scheduler/scheduler_test.go` (`synctest.Test`, `time.Sleep`+`synctest.Wait()`, `atomic.Int32` counters, `cancel()` then `<-done`); `core/arbiter/arbiter_test.go` (white-box `package arbiter`, fake workers recording call counts/concurrency via atomics). The turn-tier test is white-box (`package turntier`) so it can drive `tryConsume` and the unexported `run` if needed, and uses `synctest` for the daily-reset / cadence assertions. [Source: core/scheduler/scheduler_test.go, core/arbiter/arbiter_test.go]
- **Reflex-tier fence to respect** — `core/scheduler/imports_test.go` (`TestReflexTierIsLLMFree`) walks `core/scheduler` + `core/reflexes` and fails on a `/broker` or `/worker` import; it has a `scanned >= 3` vacuity guard. Do not add the worker path to those trees — that's why the turn tier is a separate package. [Source: core/scheduler/imports_test.go]
- **Scheduler doc already promised this story's shape** — "the turn tier (Story 3.5) layers on by registering more jobs with no change to this loop; turn jobs encode their arbiter/budget gating inside their own Run." Implement to that promise. [Source: core/scheduler/scheduler.go]

### Latest tech information

- **No new external dependency.** The turn tier uses only the stdlib (`context`, `sync`, `time`) plus the existing `core/arbiter`, `core/scheduler`, and `contracts`. `testing/synctest` (Go 1.25, already used by the scheduler/arbiter tests) drives the fake clock for the cadence/budget-reset tests. Nothing to `go get`; no `go.mod` change. [Source: core/scheduler/scheduler_test.go, go.mod (go 1.25)]

### Project Structure Notes

- New: `core/turntier/turntier.go`, `core/turntier/turntier_test.go`.
- Unchanged: `core/scheduler/*` (Yui's condition — zero diff), `core/reflexes/*`, `core/arbiter/*`, `core/dispatch/*`, `contracts/*`, `worker/*`, `broker/*`, `transport/*`, `cmd/shelldon/main.go`. No `go.mod` change.
- `.golangci.yml` unchanged. No new import fence needed: the turn tier is *allowed* to reach the worker (via the arbiter); the existing reflex-tier fence already protects the reflex tier.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 3.5] — the two ACs (budget/cooldown gate blocks with no worker invocation; turn tier added without refactoring the reflex loop)
- [Source: ...ARCHITECTURE-SPINE.md#AD-13] — scheduler tiers; turn jobs cost an LLM turn, cooldown-gated, budget + battery-aware; go through the arbiter; scheduler never invokes the worker directly
- [Source: ...ARCHITECTURE-SPINE.md#AD-8] — arbiter ≤1-in-flight; daily credit/turn budget + battery-aware backoff for turn jobs
- [Source: epics.md#NFR14, #FR10] — bounded background LLM spend + battery-aware autonomy; turn-tier scheduler
- [Source: core/scheduler/scheduler.go, core/scheduler/scheduler_test.go] — the host scheduler (unchanged) + the synctest test pattern to mirror
- [Source: core/scheduler/imports_test.go] — the reflex-tier LLM-free fence that dictates a separate turn-tier package
- [Source: core/arbiter/arbiter.go, core/arbiter/arbiter_test.go] — the submit seam + fake-worker test pattern
- [Source: contracts/job.go] — the Job the turn submits

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow)

### Debug Log References

- `go test -race ./...` → 86 passed in 20 packages
- `go test -race ./core/turntier/` → 7 passed
- `CGO_ENABLED=0 go build ./...` (native) → success; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` → success
- `golangci-lint run` → 0 issues
- Yui's condition: `git status --short core/scheduler core/reflexes` → empty (zero diff)
- Reflex-tier fence: `go test ./core/scheduler/ -run TestReflexTierIsLLMFree` → pass

### Completion Notes List

- **Turn-tier package (`core/turntier`).** New core package housing the turn-tier spend-control machinery: a shared daily `Budget`, a `Power` battery-gate seam (`ACPower` default), a narrow `Submitter` seam over the arbiter, and a `Job` that produces an ordinary `scheduler.Job`. It lives outside the reflex-tier fence trees because a turn job must reach the worker (via the arbiter); the reflex tier stays LLM-free.
- **AC1 — gates block with no worker invocation.** `Job.run` checks **cooldown → battery → budget** and returns before `arbiter.Submit` on any block, so the worker's `AssembleAndPropose` is never reached. Proven with a real `arbiter.New(countingWorker, time.Minute)` under `testing/synctest`: budget-exhausted → 0 calls; cooldown active → 1 call (first fire only); budget cap (2/day) → 2 calls; battery gate (`lowPower`) → 0 calls. Gate order ensures a cooldown/battery skip never burns budget.
- **AC2 — Yui's condition, literally.** The turn job registers as a plain `scheduler.Job` into a real `scheduler.New()`; `core/scheduler/scheduler.go` and `core/reflexes` have a **zero diff** (verified via `git status`), and `TestReflexTierIsLLMFree` still passes. The reflex tier stays LLM-free while the turn tier carries the worker path in its own package.
- **Daily budget reset.** `Budget.tryConsume(now)` keys on `year*1000+yearDay`, resetting at the calendar-day boundary; `now` is injected so synctest drives it deterministically. Proven by `TestBudgetResetsDaily` (1/day budget fires twice across a 25h synctest span) and the direct `TestBudget_TryConsumeResetsOnDayBoundary` unit test.
- **Battery seam, no hardware.** `Power` interface + `ACPower` (always allow) default; a `nil` Power defaults to `ACPower` (`TestNilPowerDefaultsToAC`). The real PiSugar2 reader arrives at Epic 6 (Story 6.3).
- **No `main` wiring (as scoped).** No real turn job exists until Story 3.6 (proactive ping) / Epic 4 (dream); 3.6 will construct the shared `Budget`/`Power`, build the proactive-ping `Job`, and register it in `main`. Per the story, AC2 is test-proven via a real `scheduler.New()`. `cmd/shelldon/main.go`, the arbiter, dispatch, the worker, the broker, and contracts are all unchanged.

### File List

- `core/turntier/turntier.go` (new)
- `core/turntier/turntier_test.go` (new)
- `_bmad-output/implementation-artifacts/3-5-turn-tier-scheduler-budget-battery-gate.md` (story tracking)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (status → review)

## Change Log

- 2026-06-22: Implemented the turn tier (`core/turntier`) — a shared daily budget, a battery-aware Power gate, and a cooldown-gated `Job` that submits through the arbiter (≤1 in flight). Gating lives in the job's Run, so the reflex-tier scheduler loop is untouched (Yui's condition, zero diff). Bounds background LLM spend (NFR14/AD-8/AD-13). Both ACs satisfied; status → review.
