---
baseline_commit: ac11f997adf6bd1e9f1edb91c35ece57ffd337bc
---

# Story 3.6: LLM-driven proactive pings

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want the pet to sometimes message me first, gated by cooldown and budget,
so that it acts proactively without spamming or overspending (FR4, AD-8).

## Context

**Sixth and final story of Epic 3 (M1 — "The Brain").** Story 3.5 built the **turn tier**: gated `turntier.Job`s (cooldown + daily budget + battery) that submit through the arbiter. This story builds the **first real turn job** on that machinery — an **LLM-driven proactive ping** — and wires it into `main`, closing the epic. When the proactive job fires (cooldown elapsed, budget available, on power), it submits a proactive-prompt turn through the arbiter → the real worker (3.3) → the broker (3.2) → an LLM reply, then **publishes that reply as an outbound message** so the selected transport (CLI or Telegram, 3.4) delivers it to the owner. There is **no preceding owner input** — this is the pet initiating contact (FR4).

**The one gap 3.5 left, by design.** `turntier.Job.run` currently *discards* the arbiter result (`_, _ = j.cfg.Arbiter.Submit(...)`) — 3.5's own comment says *"a proactive turn's outbound arrives with Story 3.6."* A reply turn's outbound is dispatch's job (it publishes the worker's reply); but a **proactive** turn has no inbound and no dispatch path, so its result must be published by the turn job itself. This story adds a **minimal, additive `OnResult` sink to `turntier.Config`** — nil-safe, so 3.5's tests stay green — and the proactive job's `OnResult` publishes the reply as a `KindOutboundMessage` envelope. This keeps the gates DRY (reuse `turntier`, don't re-implement cooldown/budget) while letting the proactive behavior decide what to do with the result.

**Reuse, don't reinvent.** The cooldown + budget + battery gating is already `turntier` (3.5). The arbiter is the ≤1-in-flight submit seam (1.3/2.6). The worker is the real LLM worker (3.3). The bus + transports already carry `OutboundMessage` to the owner (1.5/3.4). This story is the thin proactive-ping behavior that composes them: a `core/proactive` package that builds a gated `turntier.Job` whose `Build` produces a proactive-prompt `Job` and whose `OnResult` publishes the reply.

**Degrade quietly (AD-8).** A proactive turn whose worker errors (no API key, provider chain exhausted) or returns an empty reply **publishes nothing** — the pet simply doesn't ping. No reflex-ack for proactive (unlike a reply, a missed self-initiated ping needs no acknowledgement). So a keyless pet never spam-pings errors; it just stays quiet until it has something to say.

**This story does NOT:**
- change the arbiter, the worker, the broker, dispatch, the bus, the scheduler, or the transports — it adds `core/proactive`, one additive `turntier.Config` field (`OnResult`), and `main` wiring
- build presence detection / BLE-triggered greetings (FR4's presence arm is Epic 6, BLE plugin) — this is the **time/cadence-driven** proactive arm
- add a dream/reflection turn job (Epic 4) — those are separate `turntier.Job`s built later on the same machinery
- persist proactive state across restarts — cooldown/budget live in memory (reset on restart is acceptable for M1; durable budgets are not required by either AC)
- mint envelope/turn ids or add a supervised edge — the proactive job runs inside the existing reflex-scheduler edge's goroutines (2.5/3.5)

## Acceptance Criteria

1. **Proactive ping initiates an outbound message.**
   **Given** no preceding owner input
   **When** a proactive-ping turn job fires within its cooldown and daily budget
   **Then** the pet initiates an outbound message (FR4) — the job submits a proactive turn through the arbiter to the worker, and the worker's reply is published as a `KindOutboundMessage` for the owner's conversation, which the selected transport delivers. (Proven against a fake worker returning a canned reply, wiring the real bus + arbiter + scheduler + turntier, so no real credit burns.)

2. **Cooldown suppresses a too-soon ping.**
   **Given** a proactive ping recently sent
   **When** another would fire before the minimum-interval cooldown elapses
   **Then** it is suppressed by the cooldown gate (AD-8) — no second turn is submitted and no second outbound message is published until the cooldown elapses.

## Tasks / Subtasks

- [x] **Task 1 — Expose the turn result: additive `OnResult` on `turntier.Config`** (AC: 1)
  - [x] Add `OnResult func(ctx context.Context, res contracts.Result, err error)` to `turntier.Config` (optional). In `turntier.Job.run`, replace `_, _ = j.cfg.Arbiter.Submit(...)` with `res, err := j.cfg.Arbiter.Submit(ctx, j.cfg.Build())` and, **after** submit, `if j.cfg.OnResult != nil { j.cfg.OnResult(ctx, res, err) }`. This is **purely additive** — 3.5's tests (which never set `OnResult`) must still pass unchanged.
  - [x] Update the `run` doc comment: the result now flows to `OnResult` (a reply turn still has none; a proactive turn's `OnResult` publishes the outbound — Story 3.6). Keep the gate order (cooldown → battery → budget → submit) and the "every skip returns before Submit" guarantee intact.

- [x] **Task 2 — The proactive-ping behavior (`core/proactive/proactive.go`)** (AC: 1, 2)
  - [x] New package `proactive` under `core/`. It imports `contracts`, `core/bus`, `core/turntier`, `core/scheduler` (for the `Job` shape it returns), and stdlib. It does **not** import `worker`/`broker` (it submits through the `turntier.Submitter` seam and publishes to the hub), so it sits cleanly in core as a behavior, not an LLM edge.
  - [x] `const proactivePrompt` — a short instruction making the worker generate a self-initiated check-in (tunable story-time config; e.g. "Send a brief, warm, in-character check-in to your owner. One or two sentences."). The monolith worker (3.3) prepends its system persona and runs this as the user turn.
  - [x] `func NewJob(hub *bus.Hub, arb turntier.Submitter, budget *turntier.Budget, power turntier.Power, ownerConvoID string, cadence func() time.Duration, cooldown time.Duration) scheduler.Job` — builds a `turntier.Job` via `turntier.NewJob(turntier.Config{...})` and returns its `.Scheduler()`. `Build` returns `contracts.Job{Input: proactivePrompt, ConvoID: ownerConvoID}`. `OnResult` publishes on success.
  - [x] **`OnResult` publishes the reply (AC1) / stays quiet on failure (AD-8):** if `err != nil || res.Reply == ""` → return (no ping). Else `hub.Publish(contracts.Envelope{Header: {Kind: contracts.KindOutboundMessage, Src: "core", Dst: "owner"}, Payload: contracts.OutboundMessage{ConvoID: ownerConvoID, Text: res.Reply}})` — mirroring `dispatch.publishReply`. The hub routes by `Kind`, so the selected transport's outbound consumer receives it (the `Dst` header is cosmetic, as in dispatch).
  - [x] Package doc: the time-driven proactive-ping turn job (FR4); a `turntier.Job` (cooldown/budget/battery-gated, ≤1-in-flight via the arbiter) whose result is published as an outbound message; the pet stays quiet on a failed/empty turn (AD-8).

- [x] **Task 3 — Wire the proactive ping into `main.go`** (AC: 1, 2)
  - [x] Construct the shared turn-tier `budget := turntier.NewBudget(proactiveBudgetPerDay)` and `power := turntier.ACPower{}` (deferred here from 3.5; the PiSugar2 reader replaces `ACPower` at Epic 6). Add tunable consts: `proactiveBudgetPerDay` (e.g. 6), `proactiveCadence` (how often to *consider*, e.g. 30m), `proactiveCooldown` (min interval, e.g. 2h) — conservative so the pet never spams/overspends (FR4).
  - [x] Resolve the owner conversation id for the **selected transport**: CLI renders any `OutboundMessage.Text` regardless of `ConvoID`, so `"cli"` works; Telegram parses `ConvoID`→chat id, so use `SHELLDON_TELEGRAM_OWNER_ID` when `SHELLDON_TRANSPORT=telegram`. A small `ownerConvoID` resolution beside the existing transport-selection switch.
  - [x] Register the proactive job into the **existing** scheduler alongside the reflexes: `sched.Register(proactive.NewJob(hub, arb, budget, power, ownerConvoID, func() time.Duration { return proactiveCadence }, proactiveCooldown))`. No new edge, no scheduler change (it's just another `scheduler.Job`). Update the `main` package doc to note the scheduler now runs the proactive turn job alongside the reflex jobs.
  - [x] Note: with a keyless broker the proactive turn errors at the worker → `OnResult` publishes nothing (AD-8) — the pet stays quiet, never spamming errors. No extra handling needed.

- [x] **Task 4 — Tests (stdlib + testing/synctest, no testify)** (AC: 1, 2)
  - [x] **`core/turntier/turntier_test.go` — `OnResult` fires (additive):** add a test that a `turntier.Job` with an `OnResult` set receives the worker's result after a successful submit (and is not called when a gate blocks — no submit, so either not called or called consistently with your chosen contract; specify: `OnResult` is invoked only when a turn is actually submitted). Confirm all **existing** 3.5 turntier tests still pass unchanged.
  - [x] **`core/proactive/proactive_test.go` — AC1 (fires → outbound):** wire the real `bus.New()` with a registered outbound channel, a real `arbiter.New(fakeWorker, time.Minute)` where `fakeWorker` returns a canned `Result{Reply: "hi there"}`, a `turntier.NewBudget(large)`, `ACPower{}`, and register `proactive.NewJob(...)` into a real `scheduler.New()`. Run under `synctest`; advance past one cadence; assert an `OutboundMessage` with `Text == "hi there"` and `ConvoID == ownerConvoID` lands on the outbound channel. (Mirror `core/scheduler/scheduler_test.go` synctest + counting and `core/arbiter/arbiter_test.go` fake-worker patterns.)
  - [x] **AC2 (cooldown suppresses):** cadence 1s, cooldown 10s, large budget; advance 5s; assert **exactly one** outbound message was published (the first fire; the next four are inside the cooldown). This proves the cooldown gate suppresses too-soon pings.
  - [x] **Quiet on failure (AD-8):** a `fakeWorker` returning an error (or empty reply); advance; assert **no** outbound message is published — the pet stays quiet rather than spamming errors.
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; the reflex-tier fence (`TestReflexTierIsLLMFree`) and the 3.5 turntier tests still pass.

## Dev Notes

### Architecture constraints (binding)

- **FR4 — proactive action (LLM-driven arm).** "The pet acts proactively — initiates behavior." Epic 2 delivered the reflex-driven proactive mechanism; this story delivers the **LLM-driven** proactive ping on the turn tier. The presence-triggered arm (BLE) is Epic 6. [Source: epics.md#FR4, #FR Coverage Map, #Story 3.6]
- **AD-8 — The arbiter governs the brain; proactive turns are cooldown-gated; a failed call degrades.** "proactive turns (CAP-4) are gated by a **minimum-interval cooldown**"; "All turn-jobs … are gated by a **daily credit/turn BUDGET** and battery-aware backoff"; "on provider-chain exhaustion the arbiter **falls back to a reflex behavior** so the pet never freezes." For a *proactive* turn, a failed/empty result simply produces no ping (no outbound) — the cooldown + budget bound spend, the arbiter bounds concurrency. [Source: ARCHITECTURE-SPINE.md#AD-8]
- **AD-13 — Scheduler turn tier; scheduler-proposed turns go through the arbiter.** "turn jobs (reflection, dreaming, **proactive pings**) each cost a worker invocation + LLM, are few, cooldown-gated, and draw on the daily credit/turn BUDGET"; "Scheduler-proposed turn jobs go through the **arbiter** … the scheduler never invokes the worker directly." The proactive job is exactly such a turn job, built on the 3.5 turn tier. [Source: ARCHITECTURE-SPINE.md#AD-13]
- **AD-6 — Core is the sole writer / publisher; the worker only proposes.** The worker returns a proposed reply; **core publishes** it as an outbound message. The proactive job's `OnResult` (in core) does the publish — the worker never publishes. [Source: ARCHITECTURE-SPINE.md#AD-6, core/dispatch/dispatch.go]
- **AD-12 — Transport-agnostic outbound.** The proactive ping publishes a `contracts.OutboundMessage`; the selected transport (CLI/Telegram) renders it. The proactive behavior is transport-agnostic; only the `ownerConvoID` is transport-resolved in `main`. [Source: ARCHITECTURE-SPINE.md#AD-12, transport/cli/cli.go, transport/telegram/telegram.go]
- **NFR14 — bounded background spend.** The shared `turntier.Budget` + per-job cooldown bound proactive LLM spend; battery-aware via the `Power` seam. [Source: epics.md#NFR14, core/turntier/turntier.go]

### Key design decisions

- **Reuse the 3.5 turn tier; add only an `OnResult` sink.** The gates are already correct in `turntier`. The single missing piece is getting the turn result out — so add a nil-safe `OnResult` callback (additive; 3.5 tests untouched). The proactive package supplies an `OnResult` that publishes. This is DRY and keeps `turntier` generic (it still doesn't know about the bus or outbound messages).
- **`core/proactive` is a behavior, not an edge.** It composes `turntier` + the hub; it submits through the `turntier.Submitter` seam (the arbiter) and publishes to the bus. It does not import `worker`/`broker`, so it stays a clean core behavior. It's outside the reflex-tier fence (which only guards `core/scheduler`/`core/reflexes`), and it doesn't reach the LLM path directly anyway.
- **Publish-on-success, quiet-on-failure.** A proactive turn that fails or is empty publishes nothing (AD-8) — unlike a *reply* turn (dispatch acks a failure with a reflex `"…"`), a missed self-initiated ping needs no acknowledgement. This prevents a keyless pet from spam-pinging error placeholders.
- **Owner ConvoID is transport-resolved in `main`, not in the behavior.** CLI ignores `ConvoID` (prints any text); Telegram needs the owner chat id. Keeping the resolution in `main` (beside the transport switch) keeps `core/proactive` transport-agnostic. M1 single-owner; multi-owner keying is Epic 4+/post-MVP.
- **Conservative tunables, enabled by default.** `proactiveBudgetPerDay`, `proactiveCadence` (consider interval), `proactiveCooldown` (min interval) are consts in `main` set so the pet pings rarely (FR4 "without spamming or overspending"). In-memory cooldown/budget reset on restart — acceptable for M1.

### Previous story intelligence (Epic 1–3.5)

- **The turn tier is the host machinery** — `core/turntier`: `NewBudget(perDay)`, `ACPower{}`, `Submitter` seam (the arbiter satisfies it), `Config{Name, Cadence, Cooldown, Build, Arbiter, Budget, Power}`, `NewJob(cfg).Scheduler()` → a `scheduler.Job`. `Job.run` gates cooldown → battery → budget → `Arbiter.Submit`, currently discarding the result. Add `OnResult` here. [Source: core/turntier/turntier.go]
- **The arbiter is the submit seam** — `arbiter.New(worker, timeout)`; `Submit(ctx, contracts.Job) (contracts.Result, error)`; ≤1-in-flight, timeout-bounded, panic-recovering. A rejected proactive turn (ErrTurnInFlight) just means no ping this tick — the cadence retries. [Source: core/arbiter/arbiter.go]
- **dispatch shows the publish pattern** — `dispatch.publishReply(convoID, text)` builds `Envelope{Header:{Kind: KindOutboundMessage, Src:"core", Dst:"cli"}, Payload: OutboundMessage{ConvoID, Text}}` and `hub.Publish`es it. Mirror this in `OnResult` (Dst cosmetic; hub routes by Kind). dispatch is the *reply* path (inbound→submit→publish); proactive is the *self-initiated* path (cadence→gate→submit→publish). [Source: core/dispatch/dispatch.go]
- **The worker generates the reply** — `monolith.Worker.AssembleAndPropose` (3.3) prepends a system persona to `Job.Input` and calls the broker; for a proactive ping, `Input = proactivePrompt`. With no key the broker returns `ErrAllProvidersFailed` → the proactive turn errors → no ping (AD-8). [Source: worker/monolith/monolith.go, broker/broker.go]
- **The transports render outbound** — `transport/cli` prints any `OutboundMessage.Text`; `transport/telegram` parses `ConvoID`→chat id and sends. The proactive ping reaches whichever is selected in `main`. [Source: transport/cli/cli.go, transport/telegram/telegram.go]
- **main wiring pattern** — the scheduler is built with `scheduler.New()`, `sched.Register(scheduler.Job{...})` for blink + mood-drift, then guarded as the `reflex-scheduler` edge. Register the proactive job the same way (it's a `scheduler.Job`). The arbiter (`arb`) and hub are already constructed earlier in `main`. [Source: cmd/shelldon/main.go]
- **Test patterns** — `core/scheduler/scheduler_test.go` (`synctest.Test`, `time.Sleep`+`synctest.Wait()`, buffered-channel/atomic counting, `cancel()`+`<-done`); `core/arbiter/arbiter_test.go` (fake workers); `core/turntier/turntier_test.go` (real arbiter + counting worker under synctest). Mirror these for the proactive test. [Source: core/scheduler/scheduler_test.go, core/arbiter/arbiter_test.go, core/turntier/turntier_test.go]

### Latest tech information

- **No new external dependency.** The proactive ping uses only the stdlib (`context`, `time`) plus existing `core/turntier`, `core/arbiter` (via the `Submitter` seam), `core/bus`, `core/scheduler`, and `contracts`. `testing/synctest` (Go 1.25, already in use) drives the fake clock. Nothing to `go get`; no `go.mod` change. [Source: core/turntier/turntier.go, go.mod (go 1.25)]

### Project Structure Notes

- New: `core/proactive/proactive.go`, `core/proactive/proactive_test.go`.
- Modified: `core/turntier/turntier.go` (additive `OnResult` field + wire it in `run`), `core/turntier/turntier_test.go` (a test for `OnResult`), `cmd/shelldon/main.go` (construct budget/power, resolve owner ConvoID, register the proactive job, package doc).
- Unchanged: `core/scheduler/*` (still zero diff — proactive registers as a plain `scheduler.Job`), `core/reflexes/*`, `core/arbiter/*`, `core/dispatch/*`, `worker/*`, `broker/*`, `transport/*`, `contracts/*`. No `go.mod` change.
- `.golangci.yml` unchanged. No new fence: the reflex-tier fence still guards `core/scheduler`/`core/reflexes`; `core/proactive` is a turn-tier behavior allowed to drive turns.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 3.6] — the two ACs (proactive ping initiates an outbound; cooldown suppresses a too-soon ping)
- [Source: ...ARCHITECTURE-SPINE.md#AD-8] — proactive turns cooldown-gated + budget-bound; failed call degrades (no freeze)
- [Source: ...ARCHITECTURE-SPINE.md#AD-13] — proactive pings are turn-tier jobs that go through the arbiter; scheduler never invokes the worker directly
- [Source: ...ARCHITECTURE-SPINE.md#AD-6, #AD-12] — core publishes (worker only proposes); transport-agnostic outbound
- [Source: core/turntier/turntier.go] — the turn-tier machinery to extend with OnResult
- [Source: core/arbiter/arbiter.go, core/dispatch/dispatch.go] — the submit seam + the outbound publish pattern to mirror
- [Source: worker/monolith/monolith.go, broker/broker.go] — the worker/broker the proactive turn drives
- [Source: cmd/shelldon/main.go, transport/cli/cli.go, transport/telegram/telegram.go] — the wiring + transports the ping flows through

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow)

### Debug Log References

- `go test -race ./...` → 91 passed in 21 packages
- `go test -race ./core/proactive/` → 4 passed; `go test -race ./core/turntier/` → 8 passed (3.5's 7 + new OnResult)
- `CGO_ENABLED=0 go build ./...` (native) → success; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` → success
- `golangci-lint run` → 0 issues
- Yui's condition: `git status --short core/scheduler core/reflexes` → empty (scheduler itself still zero diff; only turntier got the additive OnResult)
- `go test ./core/scheduler/ -run TestReflexTierIsLLMFree` → pass

### Completion Notes List

- **Additive `OnResult` on `turntier.Config` (Task 1).** Added a nil-safe `OnResult func(ctx, contracts.Result, error)`; `Job.run` now captures the arbiter result and calls `OnResult` after a submit (never on a gated/skipped tick). 3.5's tests never set it, so all 7 still pass; a new `TestOnResult_ReceivesResultOnSubmit` proves it fires once with the worker reply under a budget of 1. The gate order and "every skip returns before Submit" guarantee are unchanged.
- **Proactive-ping behavior (`core/proactive`).** `NewJob(...)` builds a `turntier.Job` whose `Build` submits a `proactivePrompt` turn for the owner's conversation and whose `OnResult` publishes the worker's reply as a `KindOutboundMessage`. It composes the turn tier + the bus; it does not import `worker`/`broker` (submits through the `turntier.Submitter` seam, publishes via the hub) — core publishes, the worker only proposes (AD-6).
- **AC1 — proactive ping initiates an outbound.** `TestProactivePing_InitiatesOutbound` wires the real bus + arbiter (fake worker → canned reply) + scheduler + proactive job under synctest; one cadence produces one `OutboundMessage` carrying the reply for `ownerConvoID`. The pet messages first with no inbound (FR4).
- **AC2 — cooldown suppresses.** `TestProactivePing_CooldownSuppresses` (cadence 1s, cooldown 10s, 5s window) publishes exactly one ping — the cooldown gate suppresses the rest (AD-8).
- **Quiet on failure (AD-8).** `TestProactivePing_QuietOnFailure` (worker error) and `TestProactivePing_QuietOnEmptyReply` (empty reply) both publish nothing — a keyless/failed pet stays quiet rather than spamming error placeholders. No reflex-ack for a self-initiated ping.
- **main wiring (Task 3).** Constructed the shared `turntier.NewBudget(6)` + `turntier.ACPower{}` (deferred here from 3.5), resolved `ownerConvoID` beside the transport switch (CLI → "cli"; Telegram → `SHELLDON_TELEGRAM_OWNER_ID`), and registered the proactive job into the existing scheduler alongside the reflexes. Conservative tunables: 6/day budget, 30m consider-cadence, 2h cooldown. No new edge, no scheduler-loop change.
- **Yui's condition still literal.** `core/scheduler` and `core/reflexes` have a zero diff; the proactive job is just another `scheduler.Job`. Only `core/turntier` (additive) and `main` changed. Epic 3 is feature-complete: credential boundary → provider chain → real worker → second transport → turn tier → proactive pings.

### File List

- `core/proactive/proactive.go` (new)
- `core/proactive/proactive_test.go` (new)
- `core/turntier/turntier.go` (modified — additive `OnResult` field, wired in `run`)
- `core/turntier/turntier_test.go` (modified — `replyWorker` helper + `TestOnResult_ReceivesResultOnSubmit`)
- `cmd/shelldon/main.go` (modified — shared budget/power, owner-ConvoID resolution, register proactive job, package + scheduler docs)
- `_bmad-output/implementation-artifacts/3-6-llm-driven-proactive-pings.md` (story tracking)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (status → review)

### Review Findings

- [x] [Review][Defer] Budget slot consumed before Submit; transient failures permanently drain daily budget [core/turntier/turntier.go:135,143] — deferred, pre-existing by-design for M1
- [x] [Review][Defer] Telegram degraded-transport supervisor crash loop: immediate-return transportServe causes suture to restart in tight loop [cmd/shelldon/main.go:109-112] — deferred, pre-existing (3.4 scope, not previously captured)
- [x] [Review][Defer] Test outbound channel 4× larger than production (64 vs 16): tests cannot catch hub.Publish backpressure deadlock [core/proactive/proactive_test.go:37] — deferred, pre-existing

## Change Log

- 2026-06-22: Implemented LLM-driven proactive pings (`core/proactive`) on the 3.5 turn tier, with an additive `turntier.OnResult` sink to publish the turn result. The pet now messages the owner first — cooldown- and budget-gated (AD-8/NFR14/FR4) — and stays quiet on a failed/empty turn. Wired into `main` alongside the reflex jobs. Both ACs satisfied; status → review. Epic 3 feature-complete.
