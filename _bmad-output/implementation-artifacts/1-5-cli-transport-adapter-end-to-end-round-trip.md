---
baseline_commit: 02df295
---

# Story 1.5: CLI transport adapter + end-to-end round-trip

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As a developer building shelldon,
I want a CLI chat-transport adapter that emits inbound-message and consumes outbound-message envelopes over the transport-agnostic contract,
so that a message round-trips inbound→core→worker seam→stub→outbound→CLI and proves the spine is wired end-to-end (FR9, AD-12).

## Context

Fifth Epic 1 story — the **tracer bullet fires**. Stories 1.1–1.4 built the parts in isolation: the versioned `contracts/` (1.1), the core-owned `bus.Hub` point-to-point router (1.2), the `Worker` seam + `Stub` + ≤1-in-flight `arbiter` (1.3), and the `suture` supervisor root + `Guard` edge shape (1.4). **None of them are wired together yet.** This story connects them into a running process and proves the M0 spine end-to-end: a line typed at a CLI round-trips **inbound → core → worker seam → stub → outbound → CLI** and the reply renders back.

This is an **integration story**, so most of the work is wiring existing seams — but it adds two genuinely new pieces:

1. **The transport-agnostic message contract** (in `contracts/`): inbound/outbound message kinds + payload structs. AD-12 mandates the chat transport speak only this contract — `telego.Update` (or any CLI/stdin type) must **never** leak into core. This contract does not exist yet (`AllKinds` today is only `KindJob`/`KindResult`).
2. **The CLI transport adapter** (in `transport/cli/`): the first edge actor — a bus client that maps stdin lines into inbound-message envelopes and renders outbound-message envelopes to stdout. It is supervised via the 1.4 `Guard` shape.

Plus a small **core turn-dispatch loop** (`core/dispatch/`) that consumes inbound messages, runs the turn through the arbiter, and emits the reply as an outbound message — the in-core glue between the bus and the arbiter.

Keep scope tight. This is the CLI adapter + the message contract + the core dispatch glue + the end-to-end proof. It does **NOT** build the Telegram adapter (3.4 — `telego` is not imported here), the real LLM worker (3.3 — the `Stub` echo stays), prompt assembly or memory (Epic 3–4), `turn_id` idempotent-close fencing (AD-11 — deferred), the busy/offline acknowledgement for a turn rejected mid-flight (Story 2.6), multi-user/`chat_id` keying (deferred per AD-12), or any scheduler/reflex behavior (Epic 2). The stub echoes the input; the round-trip renders that echo.

## Acceptance Criteria

1. **A CLI message round-trips end-to-end and the reply renders back.**
   **Given** the CLI adapter wired to the bus as an edge actor
   **When** a CLI message is entered
   **Then** it round-trips inbound→core→worker seam→stub→outbound and the reply renders back at the CLI.

2. **No transport-specific type leaks into core.**
   **Given** the CLI adapter and core compiled together
   **When** core's imports are inspected
   **Then** no CLI- or telego-specific type leaks into core — core sees only the transport-agnostic message contract in `contracts/` (AD-12).

## Tasks / Subtasks

- [x] **Task 0 — Transport-agnostic message contract in `contracts/`** (AC: 1, 2)
  - [x] Add two `Kind` constants — `KindInboundMessage Kind = "inbound-message"` and `KindOutboundMessage Kind = "outbound-message"` — and append BOTH to `AllKinds` (in `contracts/envelope.go`)
  - [x] Add payload structs (new file `contracts/message.go`): `InboundMessage{ConvoID, Text string}` and `OutboundMessage{ConvoID, Text string}`, each with an `isPayload()` method. Doc them as the transport-agnostic message contract (AD-12): every chat transport maps its native input into `InboundMessage` and renders `OutboundMessage`; adapter-native types never cross this boundary into core. `ConvoID` is core's conversation-identity field (AD-12); the CLI maps its single conversation to a fixed id
  - [x] Register both new payloads with gob in `contracts/register.go` (`gob.Register(InboundMessage{})`, `gob.Register(OutboundMessage{})`) — they ride `Envelope.Payload` (an interface), so gob requires registration (1.1 convention)
  - [x] **Extend the required M0 round-trip test** (`contracts/contracts_test.go`): add a `cases[KindInboundMessage]` and `cases[KindOutboundMessage]` Envelope to `TestEnvelopeRoundTrip`. The test cross-checks `AllKinds`, so adding the kinds WITHOUT cases fails the suite — this keeps "every declared kind round-trips" (AC1 of 1.1, NFR9) honest as the contract grows
  - [x] `go test ./contracts/` stays green (new kinds round-trip; additive-evolution tests untouched)

- [x] **Task 1 — Core turn-dispatch loop (`core/dispatch/`)** (AC: 1, 2)
  - [x] Create `core/dispatch/dispatch.go`. Package doc: core-resident loop that consumes inbound-message envelopes, runs the turn through the arbiter (≤1 in flight, AD-8), and publishes the reply as an outbound-message envelope. It imports `contracts`, `core/bus`, `core/arbiter` — **never** `transport/*` (AD-12; enforced by Task 5). (`worker` is reached transitively via the arbiter, so dispatch does not import it directly)
  - [x] `Dispatcher` holds a constructor-injected publisher (the `*bus.Hub`), the `*arbiter.Arbiter`, and an inbound receive channel (`<-chan contracts.Envelope`). `New(hub *bus.Hub, arb *arbiter.Arbiter, inbound <-chan contracts.Envelope) *Dispatcher`
  - [x] `Serve(ctx context.Context) error` — `select` loop: on `ctx.Done()` return `ctx.Err()`; on an inbound envelope, type-assert `Payload.(contracts.InboundMessage)` (skip on mismatch — defensive), build `contracts.Job{Input: msg.Text, ConvoID: msg.ConvoID}`, call `arb.Submit(ctx, job)`, and on success publish `Envelope{Header: {Kind: KindOutboundMessage, Src: "core", Dst: "cli"}, Payload: OutboundMessage{ConvoID: msg.ConvoID, Text: res.Reply}}` via the hub
  - [x] On `arb.Submit` error (`ErrTurnInFlight` or a cancelled ctx): **skip emitting a reply** for M0 — the busy/offline acknowledgement is Story 2.6, and `turn_id` fencing is AD-11 (deferred). Do not block, do not panic
  - [x] Leave `Header.ID`/`Header.TurnID` zero for M0 (the hub routes by `Kind`; envelope-id minting + `turn_id` fencing land with the turn lifecycle, AD-11) — note this in the doc comment so it reads as deliberate, not forgotten

- [x] **Task 2 — CLI transport adapter (`transport/cli/`)** (AC: 1, 2)
  - [x] Create `transport/cli/cli.go`. Package doc: the first chat-transport edge actor (AD-12) — a bus client that publishes inbound-message envelopes and renders outbound-message envelopes. It owns its surface (stdin/stdout); it speaks only the `contracts` message contract, never leaking a CLI type to core
  - [x] `Adapter` holds the `*bus.Hub` (to publish inbound), an outbound receive channel (`<-chan contracts.Envelope`), an `io.Reader` (in) and `io.Writer` (out), and a `convoID string`. `New(hub *bus.Hub, outbound <-chan contracts.Envelope, in io.Reader, out io.Writer, convoID string) *Adapter` — `io.Reader`/`io.Writer` (not `os.Stdin`/`os.Stdout` directly) so the end-to-end test injects pipes (constructor injection, no monkeypatch)
  - [x] `Serve(ctx context.Context) error` — spawn a read-loop goroutine that scans lines from `in` with `bufio.Scanner` and publishes each as `Envelope{Header: {Kind: KindInboundMessage, Src: "cli", Dst: "core"}, Payload: InboundMessage{ConvoID: a.convoID, Text: line}}`; the main loop `select`s on `ctx.Done()` (return) and the outbound channel (type-assert `OutboundMessage`, `fmt.Fprintln(a.out, msg.Text)` with the error explicitly discarded for errcheck)
  - [x] The blocking `bufio.Scanner.Scan()` on stdin is not ctx-cancelable; on shutdown the read goroutine ends at EOF / process exit. Note this is acceptable for the M0 CLI (a real cancelable stdin is not an M0 concern) — do not build a cancelable-stdin mechanism

- [x] **Task 3 — Wire the composition root (`cmd/shelldon/main.go`)** (AC: 1)
  - [x] Update the existing `cmd/shelldon/main.go` (1.4 left it with the supervisor root and a "wire edges here in 1.5" comment). Build: `hub := bus.New()`; `arb := arbiter.New(worker.Stub{})`; two buffered envelope channels `inboundCh`/`outboundCh`; `hub.Register(contracts.KindInboundMessage, inboundCh)` and `hub.Register(contracts.KindOutboundMessage, outboundCh)` (check the returned errors)
  - [x] `disp := dispatch.New(hub, arb, inboundCh)`; `adapter := cli.New(hub, outboundCh, os.Stdin, os.Stdout, "cli")`
  - [x] Add both as supervised edges via `Guard`: `root.Add(supervisor.Guard("core-dispatch", disp.Serve))` then `root.Add(supervisor.Guard("cli-transport", adapter.Serve))`. Start order is dispatch then CLI, so the reverse-order drain (1.4) stops the CLI first, then dispatch
  - [x] Keep `root.Serve(ctx)` under the existing `signal.NotifyContext`. Supervising the core dispatch loop is consistent — core is the supervisor root and may supervise its own long-running services (a dispatch panic should restart/degrade, not kill the soul, AD-5)

- [x] **Task 4 — End-to-end round-trip test (AC: 1)** (AC: 1)
  - [x] Create `transport/cli/cli_test.go` (an integration test wiring the REAL `bus.Hub`, `arbiter`, `worker.Stub`, `dispatch.Dispatcher`, and `cli.Adapter` — no mocks; this is the spine proof). Feed input via `strings.NewReader("hello\n")`; capture output via `io.Pipe` so the reply read is a deterministic happens-before edge (no `time.Sleep`)
  - [x] Register the inbound/outbound channels on the hub, start `disp.Serve(ctx)` and `adapter.Serve(ctx)` in goroutines, then read one line from the pipe reader with `bufio` and assert it equals `"hello"` (the stub echoes the input). `cancel()` and return cleanly
  - [x] Run under `go test -race` — the hub, channels, and two edge goroutines are the cross-goroutine state

- [x] **Task 5 — Core-import-isolation test (AC: 2)** (AC: 2)
  - [x] Create `core/dispatch/imports_test.go` — a stdlib architecture test (no new dependency): with `go/parser`, walk the `core/` tree (`filepath.WalkDir("..", …)` from the test's package dir, which is `core/dispatch/`), parse every non-test `.go` file, and **fail if any import path contains `"/transport"`** (or `telego`). This mechanically enforces AC2 — core never imports a transport adapter; it sees only `contracts`
  - [x] Document why: AD-12's "no `telego.Update` into core" is otherwise only a convention; this test makes a future accidental `core → transport` import fail the build. (A `depguard` rule is the alternative; `.golangci.yml` is left to the LLM-free-core fence in Story 3.1, so the guard is a Go test here). **Verified the guard catches a real violation** (injected a temporary `core → transport/cli` import → test FAILED; removed it → PASSED)

- [x] **Task 6 — Verify build + tests + race + lint** (AC: 1, 2)
  - [x] `go build ./...` and `CGO_ENABLED=0 GOARCH=arm64 GOOS=linux go build ./...` succeed (single static binary, NFR2 — no new external dep; CLI is stdlib only)
  - [x] `go test ./...` and `go test -race ./...` pass (contracts round-trip incl. new kinds, end-to-end round-trip, import-isolation, plus all prior tests — no regressions)
  - [x] `golangci-lint run` passes (do not modify `.golangci.yml`)

### Review Follow-ups (AI)

- [x] [AI-Review][Medium] E2E test has no per-test timeout — `bufio.NewReader(pr).ReadString('\n')` blocks indefinitely if `adapter.Serve` never writes; wrap in a goroutine+select with `time.After(5*time.Second)` so a hang surfaces as a test failure rather than a CI timeout [transport/cli/cli_test.go:46] — RESOLVED: the read now runs in a goroutine guarded by a 5s `select`/`time.After`; a non-delivery fails fast with "timed out waiting for the round-trip reply", green under `-race`
- [x] [AI-Review][Low] `TurnID` field comment references `AD-12` instead of `AD-11` — AD-11 is idempotent-close/turn-lifecycle fencing; AD-12 is the transport-agnostic contract [contracts/envelope.go:42] — RESOLVED: comment corrected to AD-11 (latent doc error since Story 1.1)
- [x] [AI-Review][Low] `golangci-lint` not installed in review environment — Task 6 lint claim unverifiable; verify in CI or install locally — VERIFIED: `golangci-lint run ./...` → 0 issues (binary at `$(go env GOPATH)/bin/golangci-lint`)
- [x] [AI-Review][Low] Silent `hub.Publish` error paths — `_ = d.hub.Publish(...)` and `_ = a.hub.Publish(...)` drop `ErrNoRoute` with no log; harmless for M0 wiring but worth a slog.Warn when observability lands [dispatch.go:53, cli.go:60] — DEFERRED to deferred-work.md: routes are statically registered at startup so `ErrNoRoute` is unreachable at M0 runtime; the slog.Warn lands with AD-17 observability
- [x] [AI-Review][Low] `readLoop` goroutine not stoppable on ctx cancellation — blocks on `bufio.Scanner.Scan()` until stdin EOF; documented M0 deferral, revisit when cancelable-stdin is needed [transport/cli/cli.go:41] — DEFERRED to deferred-work.md: already an intentional, documented M0 scope decision (a cancelable stdin is not an M0 concern)

## Dev Notes

### Architecture constraints (binding)

- **Chat transport is a pluggable first-class edge actor speaking a transport-agnostic contract.** "the chat transport is a **first-class edge actor / bus client** … it emits **inbound-message** envelopes and consumes **outbound-message** envelopes, speaking a **transport-agnostic message contract in `contracts/`** … never leak `telego.Update` into core. **One adapter ships now** (Telegram via `mymmrac/telego`, or local CLI); more are added as adapters **without core change**. The adapter holds its **own** connection credential … The adapter is **supervised + auto-restarted** (AD-5)." Story 1.5 ships the **CLI** adapter (no creds needed for stdin/stdout) as a `Guard`-supervised edge. [Source: ARCHITECTURE-SPINE.md#AD-12]
- **Core owns the conversation-identity schema; adapters map native ids at the edge.** "Core owns the conversation-identity schema (the `owner` identity + the `chat_id`/`user_id` columns) … each transport adapter **maps its native id into that schema at the edge** — adapter-native ids never leak past the transport boundary into core." For M0 the CLI maps its single conversation to a fixed `ConvoID` ("cli"); multi-user `chat_id`/`user_id` keying is the deferred non-breaking add. [Source: ARCHITECTURE-SPINE.md#AD-12, #AD-7]
- **Uniform envelope contract; closed header; co-versioned payloads.** "`Envelope`/`Job`/`Result` are uniform Go structs over a core-owned in-process channel hub … each event kind is **co-versioned with a payload struct in `contracts/`** — subscribers decode the declared struct for that kind; no free-form/ad-hoc event bodies." The inbound/outbound message kinds each get a declared payload struct; the gob round-trip covers them (NFR9). [Source: ARCHITECTURE-SPINE.md#AD-4, #AD-10]
- **Core is the sole writer; the worker only proposes.** The dispatch loop runs the turn through the arbiter and emits the reply; it never lets the worker write. The `Stub` returns a proposed `Result` (echo); core publishes the outbound message. [Source: ARCHITECTURE-SPINE.md#AD-6]
- **≤1 worker turn in flight.** The dispatch loop calls `arbiter.Submit`, which admits at most one turn (1.3). For the single-user CLI, turns are naturally sequential; a turn rejected with `ErrTurnInFlight` is skipped for M0 (busy-ack is Story 2.6). [Source: ARCHITECTURE-SPINE.md#AD-8]
- **Every edge is a supervised Service.** The CLI adapter (and the core dispatch loop) run as `Guard`-wrapped suture Services under the 1.4 root, so a panic degrades/restarts rather than killing the soul. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **M0 is a real walking skeleton end-to-end through the real hub.** "M0 is a **real walking skeleton** end-to-end through the real hub, building `CGO_ENABLED=0`." The end-to-end test uses the real `bus.Hub` + `arbiter` + `Stub` — not mocks. (The on-Pi run of this round-trip is Story 1.6.) [Source: ARCHITECTURE-SPINE.md#AD-10, epics.md#Story 1.6]

### Files being modified / created (read before writing)

- **`contracts/envelope.go` (UPDATE).** Today: `Kind` is `KindJob`/`KindResult`; `AllKinds = []Kind{KindJob, KindResult}`; closed `Header` (`ID/V/Kind/Src/Dst/TurnID`); `Payload` marker interface (`isPayload()`); `Envelope{Header, Payload}`. **Change:** append two kinds to the const block and to `AllKinds`. **Preserve:** the closed header (do NOT add header fields), the `AllKinds`-derived test contract. [Source: contracts/envelope.go]
- **`contracts/register.go` (UPDATE).** Today: `Register()` gob-registers `Job{}`/`Result{}`, called from `init()`. **Change:** add the two new payloads. **Preserve:** idempotent `Register` + `init` call. [Source: contracts/register.go]
- **`contracts/contracts_test.go` (UPDATE).** Today: `TestEnvelopeRoundTrip` is a `map[Kind]Envelope` cross-checked against `AllKinds` (fails if a kind has no case). **Change:** add cases for the two new kinds. **Preserve:** the cross-check pattern and the additive-evolution tests. [Source: contracts/contracts_test.go]
- **`cmd/shelldon/main.go` (UPDATE).** Today (from 1.4): `signal.NotifyContext` → `supervisor.New("shelldon")` → `root.Serve(ctx)` with a "wire edges here in 1.5+" comment and **no edges**. **Change:** construct hub/arbiter/channels, register routes, build the dispatcher + CLI adapter, add both via `Guard`. **Preserve:** the signal-driven shutdown and the error/exit handling. [Source: cmd/shelldon/main.go]
- **`bus.Hub` (USE, do not modify).** `New()`, `Register(kind, chan<- Envelope) error` (returns `ErrDuplicateRoute`/`ErrNilDestination`), `Publish(env) error` (routes by `Kind`, `ErrNoRoute` fail-safe, **blocking** send). The dispatcher publishes outbound; the CLI publishes inbound; both register a receive channel. The blocking-`Publish` (no ctx/timeout) is intentional M0 behavior (already in deferred-work). [Source: core/bus/hub.go]
- **`arbiter.Arbiter` (USE, do not modify).** `New(worker.Worker)`, `Submit(ctx, contracts.Job) (contracts.Result, error)`. The dispatcher injects `arb` and submits each turn. [Source: core/arbiter/arbiter.go]
- **`worker.Stub` (USE, do not modify).** `Stub{}.AssembleAndPropose` echoes `turn.Input` into `Result{Reply}`. The end-to-end echo IS the proof. [Source: worker/stub.go]
- **`supervisor.Guard` / `supervisor.Root` (USE, do not modify).** `Guard(name, func(ctx) error) suture.Service` wraps an edge with its mandatory `recover()`; `root.Add(svc)` registers in start order; `root.Serve(ctx)` drains in reverse. [Source: core/supervisor/supervisor.go]

### The end-to-end wiring (the tracer path)

```
stdin line ─► cli.Adapter.readLoop ─► hub.Publish{KindInboundMessage} ─► inboundCh
                                                                            │
                                                          dispatch.Serve ◄──┘
                                                          arb.Submit(Job) ─► worker.Stub (echo) ─► Result
                                                          hub.Publish{KindOutboundMessage} ─► outboundCh
                                                                            │
                                          cli.Adapter.Serve (outbound loop) ◄┘ ─► fmt.Fprintln(out) ─► stdout
```

Two hub registrations: `KindInboundMessage → inboundCh` (core consumes), `KindOutboundMessage → outboundCh` (CLI consumes). The CLI publishes inbound + consumes outbound; core consumes inbound + publishes outbound. Symmetric, point-to-point, no broadcast (broadcast kinds arrive with plugins, Epic 6).

### Recommended shapes (minimal, idiomatic)

```go
// contracts/message.go
type InboundMessage struct {
    ConvoID string // core's conversation id (adapter maps its native id here)
    Text    string
}
func (InboundMessage) isPayload() {}

type OutboundMessage struct {
    ConvoID string
    Text    string
}
func (OutboundMessage) isPayload() {}
```

```go
// core/dispatch/dispatch.go
type Dispatcher struct {
    hub      *bus.Hub
    arb      *arbiter.Arbiter
    inbound  <-chan contracts.Envelope
}
func New(hub *bus.Hub, arb *arbiter.Arbiter, inbound <-chan contracts.Envelope) *Dispatcher {
    return &Dispatcher{hub: hub, arb: arb, inbound: inbound}
}
func (d *Dispatcher) Serve(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case env := <-d.inbound:
            msg, ok := env.Payload.(contracts.InboundMessage)
            if !ok {
                continue
            }
            res, err := d.arb.Submit(ctx, contracts.Job{Input: msg.Text, ConvoID: msg.ConvoID})
            if err != nil {
                continue // ErrTurnInFlight / cancelled: busy-ack is Story 2.6
            }
            _ = d.hub.Publish(contracts.Envelope{
                Header:  contracts.Header{Kind: contracts.KindOutboundMessage, Src: "core", Dst: "cli"},
                Payload: contracts.OutboundMessage{ConvoID: msg.ConvoID, Text: res.Reply},
            })
        }
    }
}
```

```go
// transport/cli/cli.go
type Adapter struct {
    hub      *bus.Hub
    outbound <-chan contracts.Envelope
    in       io.Reader
    out      io.Writer
    convoID  string
}
func (a *Adapter) Serve(ctx context.Context) error {
    go a.readLoop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case env := <-a.outbound:
            if msg, ok := env.Payload.(contracts.OutboundMessage); ok {
                fmt.Fprintln(a.out, msg.Text)
            }
        }
    }
}
func (a *Adapter) readLoop() {
    sc := bufio.NewScanner(a.in)
    for sc.Scan() {
        _ = a.hub.Publish(contracts.Envelope{
            Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "cli", Dst: "core"},
            Payload: contracts.InboundMessage{ConvoID: a.convoID, Text: sc.Text()},
        })
    }
}
```

### Concurrency & testing

- **End-to-end test uses the real components + `io.Pipe`, not sleeps.** Read the reply from the pipe with `bufio` — the read blocks until the CLI writes, giving a deterministic happens-before. Buffer the inbound/outbound channels (cap 1+) so the single line flows without lock-step. Mirror 1.3/1.4: channels for synchronization, `-race` clean. [Source: 1-3/1-4 dev notes]
- **No `testing/synctest`.** This is event-driven, not cadence-based (reserved for Epic 2 scheduler tests, AD-10).
- **The import-isolation test is stdlib `go/parser`** — no `golang.org/x/tools`, no `go list` exec. Walk `core/`, collect import paths, fail on `/transport`. Robust and dependency-free.
- **Blocking `hub.Publish`** is intentional M0 (deferred-work, 1.2). With buffered channels and live consumers there is no deadlock in the single-line round-trip; under shutdown a publish to an unread channel could block, but the supervised edges + reverse drain (1.4) stop consumers cleanly. Do not add ctx/timeout to `Publish` here (that is the deferred 1.2 item).

### Previous story intelligence (Stories 1.1–1.4)

- **Conventions to mirror:** package doc comment on the primary file; small files per type; **table-driven stdlib `testing` + `reflect.DeepEqual`**, no `testify`; subtests via `t.Run`; `t.Helper()` on helpers; exported sentinel errors via `errors.New` + `errors.Is`; **constructor injection, no monkeypatch** (inject `io.Reader`/`io.Writer` + channels + hub). [Source: contracts/, core/bus/, core/arbiter/, core/supervisor/]
- **Imports available:** `contracts` (`Envelope`/`Header`/`Job`/`Result` + new message types), `core/bus` (`Hub`), `core/arbiter` (`Arbiter`), `worker` (`Stub`), `core/supervisor` (`Root`/`Guard`). The dispatcher composes bus+arbiter+worker; the CLI composes bus+contracts; main composes everything.
- **No import cycle:** `core/dispatch` → {`contracts`, `core/bus`, `core/arbiter`, `worker`}; `transport/cli` → {`contracts`, `core/bus`}; `cmd/shelldon` → everything. `core/*` must NOT import `transport/*` (Task 5 enforces). Acyclic.
- **First external dependency was suture/v4 (1.4).** 1.5 adds **no** new external dependency — the CLI is stdlib (`bufio`/`io`/`os`/`fmt`). `telego` is Story 3.4.
- **`AllKinds` drives the required round-trip test** — every kind added must get a round-trip case or the suite fails (the 1.1 design, by intent). This is the one place a contracts change ripples into a test update. [Source: contracts/contracts_test.go]

### Project Structure Notes

- New packages: `core/dispatch/` (`dispatch.go`, `imports_test.go`) and `transport/cli/` (`cli.go`, `cli_test.go`); new file `contracts/message.go`. These match the Structural Seed: `transport/` = "chat-transport adapters (Telegram / CLI); bus clients" and `core/` hosts the domain glue. Do not scaffold `display/`, `broker/`, or `plugins/` (later epics). [Source: ARCHITECTURE-SPINE.md#Structural Seed]
- `.golangci.yml` unchanged this story (the transport-isolation guard is a Go test, not a lint rule).

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 1.5] — ACs, epic goal
- [Source: ...ARCHITECTURE-SPINE.md#AD-12] — chat transport pluggable first-class adapter; transport-agnostic message contract; never leak telego.Update into core; supervised + auto-restarted; conversation-identity at the edge
- [Source: ...ARCHITECTURE-SPINE.md#AD-4] — uniform Envelope; co-versioned payload per kind; transport swappable seed
- [Source: ...ARCHITECTURE-SPINE.md#AD-6] — core sole writer; worker proposes
- [Source: ...ARCHITECTURE-SPINE.md#AD-8] — ≤1 worker turn in flight (arbiter)
- [Source: ...ARCHITECTURE-SPINE.md#AD-5] — every edge a supervised Service (Guard)
- [Source: ...ARCHITECTURE-SPINE.md#AD-10] — M0 walking skeleton end-to-end through the real hub; required tests
- [Source: ...ARCHITECTURE-SPINE.md#AD-11] — turn_id idempotent close via context (deferred — IDs/fencing not minted here)
- [Source: contracts/envelope.go, contracts/register.go, contracts/contracts_test.go, core/bus/hub.go, core/arbiter/arbiter.go, worker/stub.go, core/supervisor/supervisor.go, cmd/shelldon/main.go] — existing seams this story wires
- [Source: _bmad-output/specs/spec-shelldon-go/SPEC.md] — FR9 (pluggable transport)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Opus 4.8, 1M context) via Claude Code dev-story workflow.

### Debug Log References

- TDD: extended `contracts/contracts_test.go` with the two new-kind round-trip cases first (RED — `KindInboundMessage`/`KindOutboundMessage` undefined), then added the kinds + `message.go` + gob registration to GREEN. Wrote the end-to-end test (`transport/cli/cli_test.go`) next (RED — `core/dispatch` + `transport/cli` packages absent), then implemented the dispatcher, CLI adapter, and main wiring to GREEN.
- errcheck flagged the unchecked `fmt.Fprintln` in `cli.go`; resolved by explicitly discarding (`_, _ =`).
- Verified the AC2 import guard is not vacuous: injected a temporary `core/dispatch → transport/cli` import and confirmed the test FAILED, then removed it and confirmed PASS.
- Sanity-ran the real binary: `printf 'ping shelldon\nsecond line\n' | shelldon` echoed both lines and shut down cleanly on SIGTERM — the M0 walking skeleton runs end-to-end, not only in tests.

### Completion Notes List

- **AC1 satisfied (end-to-end round-trip).** `TestEndToEndRoundTrip` wires the REAL `bus.Hub`, `arbiter`, `worker.Stub`, `dispatch.Dispatcher`, and `cli.Adapter` (no mocks), feeds `"hello\n"` via `strings.Reader`, and reads the reply back through an `io.Pipe` — asserting the stub echo round-trips inbound→core→worker seam→stub→outbound→CLI. Green under `-race`. The real binary was also run manually (two lines echoed, clean SIGTERM).
- **AC2 satisfied (no transport leak into core).** `core/dispatch/imports_test.go` walks the `core/` tree with `go/parser` and fails on any import path containing `/transport` or `telego`. Verified it catches a real violation (temporary injected import → FAIL). Core sees only the transport-agnostic message contract in `contracts/`.
- **New message contract (AD-12).** Added `KindInboundMessage`/`KindOutboundMessage` (+ `AllKinds`), `contracts.InboundMessage`/`OutboundMessage` payloads, and gob registration. The required M0 round-trip test (`AllKinds`-derived) was extended with cases for both — keeping "every declared kind round-trips" (NFR9) honest as the contract grew.
- **Core dispatch glue (`core/dispatch`).** A `Dispatcher` consumes inbound envelopes, runs each turn through the arbiter (≤1 in flight), and publishes the reply as an outbound envelope. `worker` is reached transitively via the arbiter, so dispatch imports only `contracts`/`core/bus`/`core/arbiter`.
- **CLI adapter (`transport/cli`).** The first edge actor: a read-loop publishes stdin lines as inbound messages; the `Serve` loop renders outbound messages to stdout. `io.Reader`/`io.Writer` are injected (constructor injection) so the test wires pipes. Run as a `Guard`-supervised edge.
- **Composition root.** `cmd/shelldon/main.go` now wires hub + arbiter + stub + dispatch + CLI and adds the dispatcher and CLI as `Guard`-supervised edges (start order dispatch→CLI, so reverse drain stops CLI first).
- **Scope held:** CLI adapter + message contract + dispatch glue + end-to-end proof only. **No** Telegram/`telego` (3.4), **no** real worker (3.3 — `Stub` echo stays), **no** `turn_id` fencing (AD-11), **no** busy/offline ack for a rejected turn (2.6 — skipped for M0), **no** multi-user `chat_id` keying, **no** scheduler/reflexes (Epic 2). `Header.ID`/`TurnID` left zero (hub routes by `Kind`; id/fencing arrive with the turn lifecycle).
- **No new external dependency** — the CLI is stdlib (`bufio`/`io`/`os`/`fmt`); the import-isolation test is stdlib `go/parser`.
- **Validation:** `go build ./...` + `CGO_ENABLED=0 GOARCH=arm64 GOOS=linux go build ./...` succeed; `go test -race -count=1 ./...` → all 7 packages pass, no data race; `golangci-lint run` → 0 issues. The M0 spine is wired end-to-end (the on-Pi run is Story 1.6).

### File List

- `contracts/envelope.go` (modified) — add `KindInboundMessage`/`KindOutboundMessage` + append to `AllKinds`
- `contracts/message.go` (new) — `InboundMessage`/`OutboundMessage` transport-agnostic payloads
- `contracts/register.go` (modified) — gob-register the two new payloads
- `contracts/contracts_test.go` (modified) — round-trip cases for the two new kinds
- `core/dispatch/dispatch.go` (new) — `Dispatcher` (inbound → arbiter → outbound)
- `core/dispatch/imports_test.go` (new) — AC2 core→transport import-isolation guard
- `transport/cli/cli.go` (new) — CLI transport `Adapter` (first edge actor)
- `transport/cli/cli_test.go` (new) — AC1 end-to-end round-trip test
- `cmd/shelldon/main.go` (modified) — wire hub/arbiter/stub/dispatch/CLI as supervised edges
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (modified) — story status → in-progress → review

## Change Log

| Date       | Change                                                                 |
|------------|------------------------------------------------------------------------|
| 2026-06-21 | Fired the M0 tracer bullet end-to-end: added the transport-agnostic message contract (`InboundMessage`/`OutboundMessage` kinds + payloads + gob), the core `Dispatcher` (inbound → arbiter → stub → outbound), and the CLI transport `Adapter` (first edge actor), wired in `cmd/shelldon/main.go` as `Guard`-supervised edges. A CLI line now round-trips inbound→core→worker seam→stub→outbound→CLI (AC1, proven with the real bus/arbiter/stub under `-race`; binary verified manually). A stdlib `go/parser` guard enforces no `core → transport` import (AC2, verified it catches a violation). No new external dependency. Build (native+arm64 static), tests, `-race`, and lint (0 issues) all green (Story 1.5). |
| 2026-06-21 | Addressed code review — 2 resolved (e2e test now timeout-guarded so a non-delivery fails fast instead of hanging; `TurnID` doc reference corrected AD-12→AD-11), 1 verified (`golangci-lint` → 0 issues), 2 deferred to deferred-work.md (silent `hub.Publish` errors — unreachable at M0, AD-17 scope; `readLoop` not ctx-cancelable — documented M0 deferral). Tests + `-race` + lint green. |
