---
baseline_commit: f2e9c31
---

# Story 1.4: suture supervisor root + soul-survives-edge-panic

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As a developer building shelldon,
I want `core` as the suture/v4 supervisor root with every edge a supervised `Service` carrying its own `recover()` + backoff and graceful shutdown,
so that a single edge panic degrades gracefully instead of killing the pet (the required soul-survives M0 test, AD-5, NFR10).

## Context

Fourth Epic 1 story. It stands up the **crash-isolation backbone** (AD-5): `core` becomes the `suture/v4` **supervisor root**, every edge runs as a supervised `Service` (`Serve(ctx) error`) with its own `defer recover()` + backoff restart, and shutdown drains edges in **reverse start order** via `signal.NotifyContext`. This story owns the **third of the four required M0 tests** — "the soul survives a single edge panic": inject a panic into one edge `Service` and assert `core` plus every other edge keep running (1.1 delivered the gob round-trip, 1.3 the ≤1-worker bound; 1.6 delivers atomic-write crash-safety + the on-Pi run).

The point of this story is the **supervisor machinery and the edge `Service` shape**, not the real edges. The real edges do not exist yet: the CLI transport is Story 1.5, the broker is 3.1, display/plugins are Epic 6, and the per-turn worker goroutine's `recover()` is part of the worker implementation (AD-2), not a long-running supervised `Service`. So — exactly as Story 1.3 shipped the `Worker` seam behind a **stub** — this story proves the supervisor with **test-double edge `Service`s** and stands up a thin composition root (`cmd/shelldon/main.go`) that 1.5 will wire real edges into.

Keep scope tight. This is the supervisor root + the edge `Service` shape + graceful reverse-order drain + the required panic test. It does **NOT** build any real edge (transport 1.5, broker 3.1, display/plugins Epic 6), wire the bus/arbiter into a running process (end-to-end is 1.5), supervise the per-turn worker goroutine (that `recover()` ships with the real worker, 3.3), implement turn-lifecycle/`turn_id` fencing (AD-11, deferred), or build full observability (AD-17 — a minimal slog `EventHook` for panics/backoff is in-scope; journald/log caps are not). It does **NOT** attempt to survive deadlocks, data races, or memory corruption — `recover()` catches none of those (see Dev Notes "What recover() does NOT catch").

## Acceptance Criteria

1. **A single edge panic does not kill core or its siblings; the panicked edge restarts.**
   **Given** core supervising multiple edge `Service`s
   **When** a panic is injected into one edge `Service` (the required soul-survives-a-single-edge-panic test, NFR10/AD-10)
   **Then** core and every other edge keep running and the panicked edge is restarted with backoff.

2. **Each edge `Service` recovers its own panic.**
   **Given** each edge `Service`'s `Serve(ctx) error`
   **When** its implementation is inspected
   **Then** a `defer recover()` is present per edge (`recover()` does not cross goroutines, AD-5).

3. **Graceful shutdown drains edges in reverse start order.**
   **Given** a running process
   **When** a shutdown signal arrives via `signal.NotifyContext`
   **Then** edges drain in reverse start order and the process exits cleanly (AD-5).

## Tasks / Subtasks

- [x] **Task 0 — Add the suture/v4 dependency** (AC: 1)
  - [x] `go get github.com/thejerf/suture/v4@latest`; commit the resulting `go.mod` + new `go.sum`. This is the project's **first** external dependency. suture/v4 is pure-Go — confirm `CGO_ENABLED=0 GOARCH=arm64 go build ./...` still succeeds (Task 5)
  - [x] Do **not** add any other dependency this story (no slog wrappers, no testify)

- [x] **Task 1 — Supervisor root with reverse-order drain** (AC: 1, 3)
  - [x] Create `core/supervisor/supervisor.go`. Package doc: `core` is the suture/v4 **supervisor root** (AD-5); every edge is a supervised `Service`; a single edge panic degrades gracefully (the soul survives any single edge failure); graceful shutdown drains in reverse start order
  - [x] `Root` wraps a `*suture.Supervisor` and holds edge `suture.ServiceToken`s **in start order**. `New(name string) *Root` builds the supervisor via `suture.New` with a `suture.Spec{ EventHook: ... }` (Task 4) — do **not** set `PassThroughPanics` (default `false` = suture recovers + restarts; that is the second net behind each edge's own `recover()`)
  - [x] `(*Root) Add(svc suture.Service)` → `r.sup.Add(svc)`, appending the returned token to the ordered slice. Edges are added **before** `Serve` (M0: a fixed startup set, no dynamic add-after-start)
  - [x] `(*Root) Serve(ctx context.Context) error` — run the supervisor under its **own** background context (`context.WithCancel(context.Background())`) via `ServeBackground`, then block on the **external** `ctx.Done()`. On shutdown: drain by iterating tokens in **reverse** order calling `r.sup.RemoveAndWait(token, drainTimeout)`; THEN cancel the supervisor context and return. Using a separate supervisor context is what makes the reverse drain deterministic — cancelling the external ctx must not let suture stop all edges concurrently before the ordered drain runs
  - [x] `drainTimeout` is a small package const (e.g. `5 * time.Second`) — bounds a stuck edge during shutdown; not a tunable invariant

- [x] **Task 2 — Required soul-survives-a-single-edge-panic test** (AC: 1, 2)
  - [x] In `core/supervisor/supervisor_test.go`, define a test-double edge `Service` with a `defer recover()` in its `Serve` that converts a panic into an `error` return (the AC2 per-edge pattern, via `Guard`). Drive behavior via channels/atomics — **no `time.Sleep`**, mirror 1.3's `entered`-channel happens-before discipline
  - [x] **Panic-once-then-succeed** double: panics on its first `Serve`, runs normally (blocks on `ctx.Done()`) on every subsequent `Serve`. Track restarts with an `atomic.Int32`. A sibling "healthy" double increments a heartbeat counter and stays up
  - [x] Assert: after the panic, (a) the supervisor `Serve` is still running, (b) the **sibling edge keeps running** (undisturbed — no second start), and (c) the **panicked edge is restarted** (restart count ≥ 2 starts / its `Serve` is invoked again). Panic-once (not repeatedly) so the restart is observed immediately without tripping suture's failure-backoff threshold (~15s)
  - [x] Cancel the external context at the end and assert `Root.Serve` returns cleanly

- [x] **Task 3 — Reverse-order drain test** (AC: 3)
  - [x] A "recorder" edge double appends its name to a shared slice (mutex-guarded) when its `Serve` returns due to `ctx` cancellation. Add three in a known order (e.g. `e1, e2, e3`)
  - [x] Start `Root.Serve` in a goroutine; once all three are up (happens-before via an `up` channel per edge), cancel the external context. Assert the recorded stop order is the **reverse** of start order (`e3, e2, e1`) — `RemoveAndWait` waits for each `Serve` to return before draining the next, so the order is deterministic, not timing-dependent
  - [x] Assert `Root.Serve` returns without error after the drain

- [x] **Task 4 — Minimal panic/backoff logging via suture EventHook** (AC: 1)
  - [x] Wire a `suture.EventHook` on the `Spec` that logs `suture.EventTypeServicePanic` and `suture.EventTypeBackoff` via stdlib `log/slog` (AD-17 observability — panics and edge restarts are logged events). Keep it to one small function; full journald/log-cap config is deferred (AD-17 is otherwise out of scope here)
  - [x] Do not assert on log output in tests (no log-capture coupling) — the EventHook exists for ops, the restart behavior is what the test asserts
  - [x] `Guard`'s own `recover()` also logs the recovered panic via slog, since a panic it converts to an error never reaches the suture `EventServicePanic` hook

- [x] **Task 5 — Composition root + build/lint** (AC: 1, 2, 3)
  - [x] Create `cmd/shelldon/main.go` — the thin composition root: `ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer stop()`, build `supervisor.New("shelldon")`, then `root.Serve(ctx)`. **No real edges are wired yet** (1.5 adds the CLI transport edge; bus/arbiter wiring is 1.5) — a comment marks the wiring point. Keep main ~15 lines; all tested logic lives in `core/supervisor`
  - [x] `go build ./...` and `CGO_ENABLED=0 GOARCH=arm64 go build ./...` succeed (single static binary path stays green, NFR2)
  - [x] `go test ./...` and `go test -race ./...` pass (the supervisor + edges are the cross-goroutine state under test)
  - [x] `golangci-lint run` passes (do not modify `.golangci.yml`; the LLM-free-core depguard fence lands in Story 3.1)

### Review Findings

- [x] [Review][Defer] `<-errCh` has no post-drain timeout [supervisor.go:69] — deferred; after all edges are removed and supervisor context is cancelled, `<-errCh` blocks with no timeout; a suture internal bug could hang the process indefinitely. Out of M0 scope.
- [x] [Review][Defer] Test channel receives have no timeout — deadlock risk [supervisor_test.go:63-70] — deferred; tests deadlock instead of failing usefully if suture delays a restart beyond the test binary timeout. Robustness improvement for future.
- [x] [Review][Defer] `logEvent` EventHook panic is unguarded [supervisor.go:108] — deferred; if slog panics inside `logEvent`, it propagates into suture's recovery machinery (no Guard wraps `logEvent`). Theoretical concern only; stdlib slog is rock-solid.
- [x] [Review][Defer] `RemoveAndWait` error silently discarded [supervisor.go:65] — deferred; a drain timeout (edge refusing to stop in 5s) produces no log and no error propagation; stuck edges are invisible to ops. Not in spec scope (AD-17 logging scope is minimal this story).
- [x] [Review][Defer] `logEvent` drops `EventStopTimeout` and other suture events [supervisor.go:109] — deferred; `EventStopTimeout` is operationally important but out of spec scope (AD-17 explicitly names only ServicePanic and Backoff for this story).

## Dev Notes

### Architecture constraints (binding)

- **suture supervises every edge; the soul survives ANY single edge failure.** "`suture/v4` supervises every edge (broker, transport, display, plugin-host, worker invocation) as a `Service` (`Serve(ctx) error`) with its own `defer recover()` + backoff restart; `core` is the supervisor root. **Invariant: the soul survives ANY single edge failure** — a dead edge degrades gracefully … `systemd Restart=always` (+ `OOMPolicy=stop`) is the outer net; only `core` death restarts the process. Graceful shutdown via `signal.NotifyContext`, draining in reverse startup order." Story 1.4 builds the supervisor root, the edge `Service` shape, reverse-order drain, and the required panic test — proven with test-double edges since real edges arrive later. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **The soul-survives-a-single-edge-panic test is a required M0 test.** "required M0 tests: … **'the soul survives a single edge panic'** — inject a panic in one edge `Service` and assert `core` + the other edges keep running (validates AD-5, since `recover()` doesn't cross goroutines)." This story owns the **third** of the four required M0 tests. [Source: ARCHITECTURE-SPINE.md#AD-10, SPEC NFR10]
- **Per-edge `recover()` because recover does not cross goroutines.** AD-1: "Go `recover()` does NOT cross goroutines." AD-5 therefore mandates each edge carry its **own** `defer recover()` — suture's default panic handling catches a panic in the `Serve` goroutine, but an edge that spawns child goroutines must recover them itself. AC2 requires the `defer recover()` pattern be present per edge. [Source: ARCHITECTURE-SPINE.md#AD-1, #AD-5]
- **Single supervised process; core is the supervisor root and imports no LLM/provider modules.** "one process. Edges are goroutine actors; `core` … is the **supervisor root**." The supervisor lives under `core/`. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **Graceful shutdown drains in reverse start order via `signal.NotifyContext`.** [Source: ARCHITECTURE-SPINE.md#AD-5, Consistency Conventions §Supervision]
- **Ownership-transfer keeps single-writer real across edges.** "Values sent over the in-process channel bus are **transfer-of-ownership**: the sender does not retain or mutate aliases after sending." This is the discipline that makes AD-5's "survives any single edge failure" not silently depend on no-data-race luck — it is the mitigation for the gap below. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions §Ownership transfer]

### What recover() does NOT catch (scope boundary — adversarial Finding S1)

The adversarial-seams review flags that AD-5's `suture` + `recover()` is **strictly weaker** than Python's process walls: "`recover()` only catches panics, not memory corruption, deadlocks, or a goroutine spinning on a lock." A goroutine edge that **deadlocks** (does not panic) or **corrupts shared memory** (a data race on a struct aliased after being sent over the bus) takes core down, and suture never fires because nothing panicked.

For this story that means:
- **In scope:** survive a single edge **panic** (the required M0 test). That is exactly what AC1 proves.
- **Out of scope (do not build):** deadlock detection, watchdogs, or memory-corruption guards. There is no AC for them and they are not M0 invariants.
- **The mitigation that IS the design:** the **ownership-transfer / no-aliasing convention** (above). It is already a Consistency Convention; this story does not need to add machinery for it, but the dev must not violate it (the supervisor must not hand an aliased mutable struct to two edges). The untrusted-worker case — the real threat — is gated to M3 by AD-3 (process wall), not solved here.

[Source: reviews/review-adversarial-seams.md#Finding S1, ARCHITECTURE-SPINE.md#Consistency Conventions]

### Why test-double edges (not real edges)

No real long-running edge exists yet: CLI transport is **1.5**, broker **3.1**, display/plugins **Epic 6**. The per-turn **worker** is a goroutine spawned by the arbiter per turn with its own `recover()` (AD-2: "goroutine, `context` timeout + own `recover()`") — that is a **turn-lifecycle** concern shipping with the real worker (3.3), **not** a long-running `suture.Service`. So 1.4 supervises **test-double** edge `Service`s, precisely as 1.3 proved the `Worker` seam with a stub. Do not invent real edges to supervise — that is 1.5+ work and would be speculative now.

### Recommended shapes (minimal, idiomatic)

```go
// core/supervisor/supervisor.go
const drainTimeout = 5 * time.Second

type Root struct {
    sup    *suture.Supervisor
    tokens []suture.ServiceToken // in start order
}

func New(name string) *Root {
    sup := suture.New(name, suture.Spec{EventHook: logEvent}) // logEvent = slog (Task 4)
    return &Root{sup: sup}
}

func (r *Root) Add(svc suture.Service) {
    r.tokens = append(r.tokens, r.sup.Add(svc))
}

// Serve runs the supervisor and, on ctx cancellation, drains edges in
// reverse start order before stopping the (now-empty) supervisor.
func (r *Root) Serve(ctx context.Context) error {
    supCtx, cancel := context.WithCancel(context.Background())
    defer cancel()
    errCh := r.sup.ServeBackground(supCtx)

    <-ctx.Done() // external shutdown signal (signal.NotifyContext at main)

    for i := len(r.tokens) - 1; i >= 0; i-- { // reverse start order
        _ = r.sup.RemoveAndWait(r.tokens[i], drainTimeout)
    }
    cancel()
    if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
        return err
    }
    return nil
}
```

```go
// A real (later) or test-double edge — the per-edge recover() pattern (AC2):
func (e *edge) Serve(ctx context.Context) (err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("edge %s panicked: %v", e.name, r) // returned → suture backs off + restarts
        }
    }()
    // ... do work until ctx.Done() ...
    <-ctx.Done()
    return ctx.Err()
}
```

```go
// cmd/shelldon/main.go — thin composition root
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    root := supervisor.New("shelldon")
    // Edges wired here in Story 1.5+ (CLI transport, …). None yet.
    if err := root.Serve(ctx); err != nil {
        slog.Error("supervisor exited", "err", err)
        os.Exit(1)
    }
}
```

### suture/v4 notes (verify against the resolved version)

- `Service` interface is `Serve(ctx context.Context) error`. `suture.New(name, Spec)` / `NewSimple(name)`; `Add(Service) ServiceToken`; `ServeBackground(ctx) <-chan error`; `Remove`/`RemoveAndWait(token, timeout)`.
- **Default panic handling:** `Spec.PassThroughPanics == false` → suture recovers a `Serve`-goroutine panic, emits `EventTypeServicePanic`, and restarts with backoff. Leave it default — it is the second net behind each edge's own `recover()`. Do **not** enable `PassThroughPanics`.
- **Backoff:** the first few failures restart promptly; only after the failure threshold (~5 within the decay window) does suture enter the ~15s backoff. **Panic once, then succeed** in the test so the restart is observed immediately and the test stays fast and deterministic.
- `EventHook` receives typed events; switch on `Event.Type()` for `EventTypeServicePanic` / `EventTypeBackoff` and log via `slog` (Task 4).
- Pure-Go, no CGo — `CGO_ENABLED=0 GOARCH=arm64` build stays a one-liner (NFR2). Confirm in Task 5.

### Concurrency & testing

- **No `testing/synctest`.** It is the tool for deterministic scheduler-**cadence** tests (Epic 2), per AD-10 — the panic-survival and reverse-drain tests are **event-based**, not cadence-based. Synchronize with channels/atomics (1.3's `entered`-channel pattern) and a happens-before edge, never `time.Sleep`. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **`-race` clean:** the supervisor goroutines + edge doubles are the only cross-goroutine state; guard shared test slices/counters with a mutex or `atomic`. `go test -race ./core/...` must pass.
- **Deterministic restart observation:** panic-once-then-succeed + an `atomic.Int32` restart counter the test polls (via a channel signal from the second `Serve` entry), so the assertion fires on a real happens-before edge, not a sleep.
- **Reverse-drain determinism:** `RemoveAndWait` blocks until each edge's `Serve` returns before draining the next — so recording stop order inside each `Serve`'s ctx-cancellation path yields a deterministic reverse sequence.

### Previous story intelligence (Stories 1.1–1.3)

- **Conventions to mirror:** package doc comment on the primary file; small files per type; **table-driven stdlib `testing` + `reflect.DeepEqual`**, no `testify`; subtests via `t.Run`; `t.Helper()` on helpers; exported sentinel errors via `errors.New` matched with `errors.Is`; **constructor injection, no monkeypatch** (the `Root` takes edges via `Add`, the way the arbiter took its `Worker` via `New`). [Source: core/arbiter/arbiter.go, core/bus/hub.go]
- **Imports available:** `github.com/elliotboney/shelldon_go/contracts`, `core/bus` (the Hub), `core/arbiter` (the ≤1 gate), `worker` (seam + stub). 1.4 does **not** wire any of these into the supervisor — it is supervisor machinery only; the end-to-end wiring (bus + arbiter + CLI transport edge) is Story 1.5.
- **No import cycle:** `core/supervisor` imports `suture` (+ stdlib `slog`/`context`/`time`); `cmd/shelldon` imports `core/supervisor`. Acyclic; nothing imports back into `cmd`.
- **First external dependency:** the repo has no `go.sum` yet — Task 0 introduces both the suture require and the generated `go.sum`. Commit both.

### Project Structure Notes

- New packages: `core/supervisor/` (`supervisor.go`, `supervisor_test.go`) and `cmd/shelldon/` (`main.go`). The Structural Seed names `core/ … suture root` and a single supervised process; `cmd/shelldon/` is the conventional Go binary entrypoint (the seed shows "one supervised Go process" but no explicit `cmd/` — adding it now is the standard composition-root location and the binary 1.6 cross-compiles). Do not scaffold other siblings. [Source: ARCHITECTURE-SPINE.md#Structural Seed]
- `.golangci.yml` unchanged this story.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 1.4] — ACs, epic goal
- [Source: ...ARCHITECTURE-SPINE.md#AD-1] — single supervised process; recover() does not cross goroutines; core is supervisor root
- [Source: ...ARCHITECTURE-SPINE.md#AD-5] — suture supervises every edge; soul survives any single edge failure; reverse-order drain
- [Source: ...ARCHITECTURE-SPINE.md#AD-2] — worker is a goroutine with its own recover() (per-turn, deferred to 3.3 — not a supervised Service here)
- [Source: ...ARCHITECTURE-SPINE.md#AD-10] — required M0 tests (soul-survives-a-single-edge-panic); synctest reserved for cadence tests
- [Source: ...ARCHITECTURE-SPINE.md#AD-17] — observability: panics + edge restarts are logged events (minimal slog EventHook in-scope)
- [Source: ...ARCHITECTURE-SPINE.md#Consistency Conventions] — §Supervision (suture Service / reverse drain / signal.NotifyContext), §Ownership transfer (no-aliasing mitigation)
- [Source: ...reviews/review-adversarial-seams.md#Finding S1] — recover() weaker than process walls; ownership-transfer rule; deadlock/corruption out of scope
- [Source: _bmad-output/specs/spec-shelldon-go/SPEC.md] — NFR10 (soul survives any single edge failure)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Opus 4.8, 1M context) via Claude Code dev-story workflow.

### Debug Log References

- Verified suture/v4 **v4.0.6** API against the module cache before coding (`Spec.EventHook`, `EventServicePanic`/`EventBackoff` fields, `ServeBackground`, `RemoveAndWait`, `Supervisor.Serve` returns `context.Canceled` on ctx-cancel). Confirmed default `PassThroughPanics == false` → suture recovers panics in the service goroutine and restarts.
- TDD: wrote `core/supervisor/supervisor_test.go` first, confirmed RED (`undefined: New`, `undefined: Guard`), then `supervisor.go` to GREEN. gofmt/vet/lint clean on first pass.

### Completion Notes List

- **AC1 satisfied (required M0 test #3 — soul survives a single edge panic).** `TestRoot_SoulSurvivesSingleEdgePanic` injects a **raw** panic (no local recover, via a test-only `serviceFunc`) into one edge while a sibling runs. The panic is isolated by the supervisor in the service goroutine (proving `recover()` does not cross goroutines, AD-5); observing the flaky edge's **second start** proves the supervisor survived and restarted it, and the sibling is asserted **undisturbed** (exactly one start). This is the third of the four required M0 tests (1.1 = gob round-trip, 1.3 = ≤1-worker; 1.6 = atomic-write + on-Pi).
- **AC2 satisfied (per-edge `defer recover()`).** `Guard(name, serveFn)` is the production edge shape — its `Serve` carries the mandatory `defer recover()` that converts a panic to an error (and logs it). `TestRoot_EdgeRecoversOwnPanicViaGuard` proves a Guard-wrapped edge recovers its own panic and is restarted. Story 1.5's CLI transport edge wraps its work in `Guard`, so every edge gets the recover() by construction.
- **AC3 satisfied (reverse-order drain).** `TestRoot_DrainsInReverseStartOrder` adds `e1,e2,e3`, cancels the external context, and asserts the recorded stop order is `e3,e2,e1`. `Root.Serve` runs the supervisor under its **own** context and, on the external `ctx.Done()`, drains tokens via `RemoveAndWait` in reverse start order **before** stopping the supervisor — deterministic, not timing-based.
- **Design call:** `Root.Serve` uses a separate supervisor context (not the external one) so cancelling the external `ctx` cannot let suture stop all edges concurrently before the ordered drain runs. A context-cancelled shutdown is normalized to a nil error (`supErr`).
- **Logging (AD-17):** the suture `EventHook` logs `EventServicePanic`/`EventBackoff` via `slog`; `Guard`'s recover() additionally logs panics it converts to errors (those never reach the suture hook). Full journald/log-cap config remains deferred.
- **Scope held:** supervisor root + edge `Service` shape (`Guard`) + reverse-order drain + the required panic test only. **No** real edges (transport 1.5, broker 3.1, display/plugins Epic 6), **no** bus/arbiter wiring (1.5), **no** per-turn worker supervision (ships with the real worker, 3.3), **no** `turn_id` fencing (AD-11). Per Finding S1, only **panic** survival is in scope — deadlock/data-race/memory-corruption survival is explicitly out of scope, mitigated by the existing ownership-transfer convention. `main` is the thin composition root with no edges wired yet.
- **First external dependency** added: `github.com/thejerf/suture/v4 v4.0.6` (pure-Go; arm64 static build stays a one-liner). `go.sum` created.
- **Validation:** `go build ./...` + `CGO_ENABLED=0 GOARCH=arm64 GOOS=linux go build ./...` succeed; `go test ./...` and `go test -race ./...` → 21 passed, no data race; `golangci-lint run` → 0 issues.

### File List

- `core/supervisor/supervisor.go` (new) — `Root` supervisor root (`New`/`Add`/`Serve` with reverse-order drain), `Guard` edge wrapper with per-edge `recover()`, `logEvent` slog EventHook
- `core/supervisor/supervisor_test.go` (new) — required soul-survives-panic test + Guard-recover test + reverse-order drain test + no-edges shutdown test
- `cmd/shelldon/main.go` (new) — composition root: `signal.NotifyContext` → `supervisor.New` → `Serve` (no edges wired yet)
- `go.mod` (modified) — add `github.com/thejerf/suture/v4 v4.0.6`
- `go.sum` (new) — checksums for suture/v4
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (modified) — story status → in-progress → review

## Change Log

| Date       | Change                                                                 |
|------------|------------------------------------------------------------------------|
| 2026-06-21 | Stood up `core` as the suture/v4 supervisor root: `Root` (`New`/`Add`/`Serve`) with graceful reverse-start-order drain, `Guard` edge wrapper carrying the per-edge `defer recover()` (AD-5), and a slog `EventHook`. Added the composition root `cmd/shelldon/main.go` (`signal.NotifyContext`). Delivered the third required M0 test — soul-survives-a-single-edge-panic — green under `-race`. First external dependency: `thejerf/suture/v4 v4.0.6`. Build (native+arm64 static), tests (21), `-race`, and lint (0 issues) all green (Story 1.4). |
