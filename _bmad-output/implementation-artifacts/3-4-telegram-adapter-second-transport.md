---
baseline_commit: ac11f997adf6bd1e9f1edb91c35ece57ffd337bc
---

# Story 3.4: Telegram adapter (second transport)

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want a Telegram chat-transport adapter that carries owner messages and replies end-to-end,
so that swapping CLI↔Telegram proves the transport is pluggable with no core change (FR9, AD-12).

## Context

**Fourth story of Epic 3 (M1 — "The Brain").** Stories 3.1–3.3 built the brain: credential boundary, provider chain, and the real worker behind the seam. This story builds the **second chat transport** — a Telegram adapter — to **prove the transport seam is genuinely pluggable** (AD-12). The whole point is the *swap*: the CLI adapter (Story 1.5) and the new Telegram adapter both speak the same transport-agnostic message contract (`contracts.InboundMessage`/`OutboundMessage`), so selecting one over the other touches **no `core/` code** — only `main` picks which edge to wire.

**The transport is a first-class edge actor (AD-12).** Like the CLI adapter, the Telegram adapter is a bus client peer to broker and display: it publishes `KindInboundMessage` envelopes and consumes `KindOutboundMessage` envelopes, runs as a supervised `Serve(ctx) error` edge under the suture root (AD-5), and **never lets a `telego` type cross into core** — it maps the native Telegram chat id into core's `ConvoID` at the edge and renders outbound replies back to the right chat. Core sees only the contract structs it already sees from the CLI.

**The adapter holds its own connection credential (AD-12 scope, not AD-9).** The Telegram bot token is the adapter's *own* surface credential, resolved from the environment inside the adapter — it is **not** a model/tool credential and never touches the broker. The broker stays the sole holder of model/tool creds (AD-9). No credential ever rides the bus (NFR8): inbound/outbound message structs carry none.

**Graceful degradation (AD-5/AD-12).** A transport failure (no token, network down, Telegram API error) **degrades to reflex-only and never crashes core** — the adapter is supervised and auto-restarted with backoff. With CLI selected (the default), the Telegram adapter isn't even constructed, so nothing regresses.

**This story does NOT:**
- change `core/`, the bus, dispatch, the arbiter, the worker, or the broker — it adds a new adapter package and a transport-selection branch in `main`
- add multi-user keying / group chat / `chat_id`/`user_id` columns — M1 is single-owner; the `chat_id`/`user_id` schema add is deferred to Epic 4 (AD-7/AD-12). The adapter maps the owner's chat id into `ConvoID` and (optionally) ignores non-owner chats
- run CLI and Telegram simultaneously — the bus is point-to-point (`ErrDuplicateRoute`); `main` selects exactly one transport. (Concurrent multi-transport fan-out is a later event-kind story.)
- stream replies, support media/inline keyboards/commands — text in, text out (the AC is "owner messages and replies round-trip")
- remove the CLI adapter — it stays as the default transport and keeps the 1.5 e2e test green

## Acceptance Criteria

1. **End-to-end round-trip over Telegram.**
   **Given** the Telegram adapter wired as an edge actor
   **When** an owner sends a Telegram message
   **Then** it round-trips through core to a reply end-to-end over Telegram (FR9) — the inbound text becomes an `InboundMessage`, core produces a reply, and the adapter sends that reply back to the originating chat. (Proven against a fake Telegram client so no real bot/network is needed, wiring the real bus + arbiter + stub — mirroring the CLI e2e test.)

2. **Swap requires no core change.**
   **Given** the CLI adapter swapped for the Telegram adapter
   **When** the swap is made
   **Then** no `core/` code changes — only the adapter is added/selected in `main` (FR9/AD-12). The `core/dispatch/imports_test.go` fence (core must not import `/transport` or `telego`) still passes, proving the swap stayed at the edge.

3. **NAT-idle watchdog keeps the long-poll alive.**
   **Given** the Telegram long-poll running
   **When** the connection idles past the NAT window
   **Then** a NAT-idle watchdog keeps the long-poll alive with a `Timeout` under the NAT window (AD-12) — the long-poll's `GetUpdates` `Timeout` is configured below a documented NAT-idle threshold so the connection refreshes before a home-router NAT mapping expires.

## Tasks / Subtasks

- [x] **Task 1 — Add the `mymmrac/telego` dependency** (AC: 1, 3)
  - [x] `go get github.com/mymmrac/telego@latest` and `go mod tidy`. Pin whatever version resolves; record it in the File List / Completion Notes. telego is **pure Go** — confirm `CGO_ENABLED=0` native + arm64 builds still succeed after adding it.
  - [x] Note: the **broker SDK fence** (`broker/imports_test.go`) lists only provider SDKs (anthropic/go-openai/ollama), so telego is unaffected. The **core fence** (`core/dispatch/imports_test.go`) already rejects `telego` and `/transport` imports inside `core/` — telego must live only under `transport/telegram` (an edge), never in core.

- [x] **Task 2 — The Telegram adapter (`transport/telegram/telegram.go`)** (AC: 1, 3)
  - [x] New package `telegram` under `transport/`, mirroring `transport/cli`'s shape (a bus-client edge actor with `Serve(ctx) error`). Package doc: the second chat-transport edge actor (AD-12); speaks only the transport-agnostic message contract; holds its own connection credential; supervised edge (AD-5).
  - [x] **Narrow client seam for testability.** Define a one-/two-method interface the adapter depends on, in **adapter-local simple types** (no telego types leak even into the test): e.g. `type client interface { Updates(ctx context.Context) (<-chan Update, error); Send(ctx context.Context, chatID int64, text string) error }` with `type Update struct { ChatID int64; Text string }`. A telego-backed impl (`telegoClient`) wraps `bot.UpdatesViaLongPolling(ctx, params)` and `bot.SendMessage(ctx, …)`; tests inject a fake. (Mirrors broker's `Provider`/`Completer` + fake pattern and monolith's `Completer`.)
  - [x] `type Adapter struct { hub *bus.Hub; outbound <-chan contracts.Envelope; c client }` plus `New(...)`. The adapter imports `contracts`, `core/bus`, and (in the telego-backed constructor only) `telego` — never `core/` internals.
  - [x] `Serve(ctx)`: start the update stream (`c.Updates(ctx)`), run a read loop that maps each `Update` → `contracts.InboundMessage{ConvoID: convoID(chatID), Text}` and publishes it as a `KindInboundMessage` envelope (`Src: "telegram", Dst: "core"`); the main select loop renders `KindOutboundMessage` envelopes by parsing `ConvoID` back to a `chatID` and calling `c.Send(ctx, chatID, msg.Text)`. Return `ctx.Err()` on shutdown. Mirror `transport/cli/cli.go` structure; cancelling ctx closes the telego updates channel (clean shutdown — better than CLI's non-cancelable Scan).
  - [x] **ConvoID mapping at the edge (AD-12).** Map the native int64 chat id to `ConvoID` (e.g. `strconv.FormatInt(chatID, 10)`) inbound and parse it back outbound. A `telego.Update` / chat-id type **never** crosses into core. Keep it single-owner for M1 (no `chat_id`/`user_id` columns — Epic 4).
  - [x] **Owner guard (minimal).** Optionally restrict to the owner's chat via `SHELLDON_TELEGRAM_OWNER_ID` (env); if set, drop inbound updates from other chats so a stranger who finds the bot can't talk to the pet. Keep it simple — a single id compare, skip if unset.
  - [x] **NAT-idle watchdog (AC3).** Configure the long-poll `GetUpdatesParams.Timeout` to a `const longPollTimeout` that is **under a documented `const natIdleWindow`** (tunable story-time config; e.g. timeout 30s, NAT window comfortably larger). Comment why: a long-poll `Timeout` shorter than the home-router NAT mapping lifetime forces a fresh `GetUpdates` before the mapping expires (AD-12 adapter detail).
  - [x] **Token = adapter's own credential (AD-12/NFR8).** Resolve `SHELLDON_TELEGRAM_TOKEN` from the env inside the telego-backed constructor; never the broker's model creds, never on the bus. Missing token → constructor/Serve returns an error and the supervised edge degrades to reflex-only (AD-5) rather than crashing core. Never log the token value.

- [x] **Task 3 — Transport selection in `main.go`** (AC: 1, 2)
  - [x] Select the transport by env: `SHELLDON_TRANSPORT` (`cli` default | `telegram`). Construct exactly one adapter, register it on the **same** `outbound` channel for `KindOutboundMessage`, and guard it under the suture root with the same `supervisor.Guard("…-transport", adapter.Serve)` shape. The CLI path stays the default so existing behavior is unchanged.
  - [x] No `core/` change. The bus is point-to-point, so only the selected adapter registers — no `ErrDuplicateRoute`. Update the `main` package doc to note the transport is now selectable (CLI default; Telegram via env).
  - [x] (Minor, optional) `core/dispatch` publishes outbound with a cosmetic `Dst: "cli"` header; the hub routes by `Kind`, so it's correct for either transport — leave it or note it. Do **not** change dispatch for this.

- [x] **Task 4 — Tests (stdlib, no testify)** (AC: 1, 2, 3)
  - [x] **`transport/telegram/telegram_test.go` — AC1 e2e:** a **fake client** delivers one `Update{ChatID: 42, Text: "hello"}` on its updates channel and **captures `Send` calls**. Wire the **real** bus + arbiter (`worker.Stub{}`) + dispatch + state (mirror `transport/cli/cli_test.go`); run `dispatch.Serve` and `adapter.Serve` as goroutines; assert the fake's `Send` receives `chatID=42` and `text="hello"` (the stub echoes input) within a timeout. This proves inbound → core → worker seam → outbound → Telegram round-trips end-to-end.
  - [x] **AC1 (ConvoID edge mapping):** assert the inbound `Update` chat id `42` became `ConvoID "42"` (or your chosen scheme) and that the outbound `Send` resolved that `ConvoID` back to `chatID 42` — proving the native id is mapped at the edge and reversed correctly, with no telego type crossing into core.
  - [x] **AC3 (NAT watchdog):** assert `longPollTimeout < natIdleWindow` (a cheap invariant test on the configured constants) so a future bump that lets the timeout exceed the NAT window fails the suite.
  - [x] **AC2 (swap stays at the edge):** `core/dispatch/imports_test.go` still passes (no `core/` package imports `/transport` or `telego`). Add nothing to core; the existing fence is the proof. (Optionally assert the adapter's `Serve` matches the `func(context.Context) error` edge shape so the `supervisor.Guard` wiring can't silently break.)
  - [x] (Optional) **Owner guard:** with `SHELLDON_TELEGRAM_OWNER_ID` set, a fake update from a non-owner chat is dropped (no inbound published); from the owner chat it round-trips. Use `t.Setenv`.
  - [x] `go test -race ./...` passes; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues; the CLI e2e test (`transport/cli/cli_test.go`) still passes (CLI unchanged).

## Dev Notes

### Architecture constraints (binding)

- **AD-12 — Chat transport is a pluggable first-class adapter.** "the chat transport is a **first-class edge actor / bus client** (peer to broker and display) — it emits **inbound-message** envelopes and consumes **outbound-message** envelopes, speaking a **transport-agnostic message contract in `contracts/`** (a Go interface; never leak `telego.Update` into core). **One adapter ships now** (Telegram via `mymmrac/telego`, or local CLI); more … are added as adapters **without core change**. The adapter holds its **own** connection credential … **Core owns the conversation-identity schema** … each transport adapter **maps its native id into that schema at the edge** … The adapter is **supervised + auto-restarted** (AD-5); a transport failure **degrades to reflex-only** and never crashes core. Telegram long-poll specifics (NAT-idle watchdog, `Timeout` under the NAT window) are adapter detail." This story is the *second* adapter that turns AD-12's "pluggable" claim into a demonstrated swap. [Source: ARCHITECTURE-SPINE.md#AD-12]
- **AD-4 — Uniform Envelope contract; transport is swappable seed.** The transport under the message seam is swappable; the adapter speaks `Envelope` + the message contract structs and the hub routes point-to-point by `Kind`. No in-process serialization. [Source: ARCHITECTURE-SPINE.md#AD-4, core/bus/hub.go, contracts/message.go]
- **AD-5 — suture supervises every edge; the soul survives any single edge failure.** The Telegram adapter is a supervised `Service` (`Serve(ctx) error`) with backoff restart; a transport-down condition degrades to reflex-only and never kills core. [Source: ARCHITECTURE-SPINE.md#AD-5, core/supervisor/supervisor.go, cmd/shelldon/main.go]
- **AD-9 scope — broker is the sole holder of MODEL/TOOL creds only.** "a chat-transport adapter owns its **own** connection credential for its own surface (AD-12) and never touches model/tool creds." The Telegram bot token is the adapter's surface credential, resolved inside the adapter, never via the broker, never on the bus (NFR8). [Source: ARCHITECTURE-SPINE.md#AD-9, #AD-12]
- **FR9 — pluggable transport.** Swapping CLI↔Telegram is a pure edge change (add/select an adapter); core is untouched, proven by the imports fence. [Source: epics.md#Story 3.4]
- **Structural Seed — `transport/`.** "`transport/` # chat-transport adapters (Telegram / CLI); bus clients, own connection cred." The new package is `transport/telegram`, sibling to `transport/cli`. [Source: ARCHITECTURE-SPINE.md#Structural Seed]
- **Deferred (do NOT build here).** Additional transport adapters / multi-user keying are explicitly future ("group chat, web, multi-user keying — are post-MVP"). M1 stays single-owner; `chat_id`/`user_id` columns land with Epic 4 memory (AD-7), as a non-breaking add. [Source: ARCHITECTURE-SPINE.md#Deferred, #AD-7]

### Key design decisions

- **Mirror `transport/cli`, don't re-architect.** The CLI adapter is the proven shape: `Adapter{hub, outbound, …}`, `New(...)`, `Serve(ctx)` with a read loop publishing inbound and a select loop rendering outbound. Copy that shape so the swap is obviously symmetric — which *is* the point of the story.
- **Narrow `client` interface + fake, not a live bot in tests.** The adapter depends on a tiny adapter-local `client` interface (simple `int64`/`string` types, no telego), so the e2e test injects a fake that feeds one update and captures sends — exactly how `cli_test` uses pipes and `broker_test` uses `fakeProvider`. The telego-backed `client` is the only place telego is touched.
- **Map native id at the edge; nothing telego crosses into core.** Inbound: `chatID int64 → ConvoID string`. Outbound: `ConvoID string → chatID int64`. Core never sees a telego type or a Telegram-native id. This is AD-12's "maps its native id into that schema at the edge."
- **NAT-idle watchdog = long-poll `Timeout` under the NAT window.** A home-router NAT mapping expires after some idle window; a `GetUpdates` long-poll `Timeout` shorter than that forces a fresh request before the mapping dies, keeping the long-poll alive. Encoded as two tunable consts with `longPollTimeout < natIdleWindow`, asserted by a test.
- **`main` selects; bus is point-to-point.** `SHELLDON_TRANSPORT` chooses the adapter; only one registers for `KindOutboundMessage` (a second would hit `ErrDuplicateRoute`). CLI stays default so nothing regresses and the 1.5 e2e stays green.
- **Token absent → degrade, don't crash.** Per AD-5, if Telegram is selected but the token is missing/invalid, the supervised edge errors and backs off (reflex-only); core keeps running. Never log the token.

### Previous story intelligence (Epic 1–3.3)

- **The CLI adapter is the template** — `transport/cli/cli.go`: `Adapter` struct holding `hub`, `outbound <-chan contracts.Envelope`, io + `convoID`; `Serve(ctx)` runs `go a.readLoop()` and a `select { ctx.Done | outbound }` render loop; `readLoop` publishes `contracts.Envelope{Header:{Kind: KindInboundMessage, Src, Dst}, Payload: InboundMessage{ConvoID, Text}}`. Mirror this exactly. [Source: transport/cli/cli.go]
- **The e2e test is the template** — `transport/cli/cli_test.go` (`TestEndToEndRoundTrip`): wires the **real** `bus.New()`, registers `inbound`/`outbound` channels, `arbiter.New(worker.Stub{}, time.Minute)`, `state.New(state.Default(), tmp)`, `dispatch.New(...)`, runs `disp.Serve` + `adapter.Serve` as goroutines, and asserts the round-trip within a 5s timeout. Mirror it with a fake telego client instead of pipes. [Source: transport/cli/cli_test.go]
- **The message contract is ready** — `contracts.InboundMessage{ConvoID, Text}` / `OutboundMessage{ConvoID, Text}` already exist and the doc-comment already names "Telegram in Story 3.4." No contract change needed. [Source: contracts/message.go]
- **The bus routes by Kind, point-to-point** — `hub.Register(kind, dst)` returns `ErrDuplicateRoute` on a second registrant; `hub.Publish(env)` routes by `env.Kind` (promoted from `Header`). Outbound `Dst` is cosmetic. So registering the Telegram adapter on the existing `outbound` channel needs no dispatch change. [Source: core/bus/hub.go, core/dispatch/dispatch.go]
- **dispatch already publishes outbound + degrades to reflex** — `dispatch.Serve` submits `Job{Input, ConvoID}` and publishes `OutboundMessage{ConvoID, Reply}` (or the `reflexAck`) on `KindOutboundMessage`. Works for any transport; no change. [Source: core/dispatch/dispatch.go]
- **main wiring pattern** — adapters are `supervisor.Guard("<name>-transport", adapter.Serve)` added to the suture `root`; the CLI adapter is `cli.New(hub, outbound, os.Stdin, os.Stdout, "cli")`. Add a selection branch before the `root.Add(...)` for the transport edge. [Source: cmd/shelldon/main.go]
- **The core fence already names telego** — `core/dispatch/imports_test.go` fails the build if any `core/` package imports `/transport` or `telego`. This *is* AC2's proof; keep telego out of core. [Source: core/dispatch/imports_test.go]
- **Test-double pattern** — small in-test structs implementing the seam interface (`fakeProvider` in broker, `fakeCompleter` in monolith, pipes in cli). Mirror for the fake `client`. [Source: broker/broker_test.go, worker/monolith/monolith_test.go]

### Latest tech information (telego)

- **Library:** `github.com/mymmrac/telego` — **pure Go**, `CGO_ENABLED=0` clean (confirmed; no cgo deps). `go get github.com/mymmrac/telego@latest`; pin the resolved version in go.mod and record it.
- **Construct:** `bot, err := telego.NewBot(token, telego.WithDefaultLogger(false, true))` (options optional; avoid debug logging the token).
- **Long-poll (current ctx-first form):** `updates, err := bot.UpdatesViaLongPolling(ctx, params)` where `params := (&telego.GetUpdatesParams{}).WithTimeout(longPollTimeoutSeconds)`. It returns a `<-chan telego.Update` immediately and polls in a background goroutine; **cancelling `ctx` closes the channel** (clean shutdown). A nil `params` defaults to an ~8s timeout — set `Timeout` explicitly for the NAT watchdog.
- **Read an update:** `if u.Message != nil { chatID := u.Message.Chat.ID /*int64*/; text := u.Message.Text /*string*/ }`; skip empty text (non-text messages).
- **Send a reply:** `bot.SendMessage(ctx, (&telego.SendMessageParams{}).WithChatID(telegoutil.ID(chatID)).WithText(text))` (import helper `tu "github.com/mymmrac/telego/telegoutil"`).
- **Version caveat:** the long-polling start/stop API changed across telego versions (older forms returned a blocking error or used a separate stop func). Use the **current ctx-first `UpdatesViaLongPolling(ctx, params, …)`** that returns `(<-chan Update, error)`. If the pinned version differs, adjust to its signature and keep the narrow `client` seam stable so the adapter/tests don't churn. [Source: research subagent — pkg.go.dev/github.com/mymmrac/telego, telego.pixelbox.dev]

### Project Structure Notes

- New: `transport/telegram/telegram.go`, `transport/telegram/telegram_test.go`.
- Modified: `cmd/shelldon/main.go` (transport-selection branch + imports + package doc), `go.mod` / `go.sum` (add telego).
- Unchanged: `core/*` (the whole tree), `transport/cli/*` (kept as default), `contracts/*`, `broker/*`, `worker/*`, reflexes, scheduler. No contract change.
- `.golangci.yml` unchanged. Verify `core` still imports neither `/transport` nor `telego` (existing fence), and that telego lives only under `transport/telegram`.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 3.4] — the three ACs (end-to-end over Telegram; swap needs no core change; NAT-idle watchdog)
- [Source: ...ARCHITECTURE-SPINE.md#AD-12] — chat transport as pluggable first-class adapter; own connection cred; map native id at edge; supervised + degrade-to-reflex; NAT-idle watchdog
- [Source: ...ARCHITECTURE-SPINE.md#AD-4, #AD-5, #AD-9] — Envelope/message contract + swappable transport; suture-supervised edges; broker-only model creds (adapter owns its own surface cred)
- [Source: transport/cli/cli.go, transport/cli/cli_test.go] — the adapter + e2e-test shape to mirror
- [Source: contracts/message.go] — the transport-agnostic InboundMessage/OutboundMessage contract (already names Story 3.4)
- [Source: core/bus/hub.go, core/dispatch/dispatch.go] — point-to-point routing + outbound publish (no change needed)
- [Source: core/dispatch/imports_test.go] — the core fence that proves AC2 (no `/transport`/`telego` in core)
- [Source: cmd/shelldon/main.go] — the wiring to add transport selection
- [Source: research subagent / pkg.go.dev/github.com/mymmrac/telego] — current telego long-poll + send API

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Code dev-story workflow)

### Debug Log References

- `go test -race ./...` → 79 passed in 19 packages
- `go test -race ./transport/telegram/` → 5 passed
- `CGO_ENABLED=0 go build ./...` (native) → success; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` → success (telego is pure Go)
- `golangci-lint run` → 0 issues
- AC2 fence: `go test ./core/dispatch/ -run TestCoreDoesNotImportTransport` → pass; `grep mymmrac/telego` over non-test source → only `transport/telegram/telegram.go`
- CLI regression: `go test ./transport/cli/` → pass (CLI unchanged, still the default)
- telego resolved + pinned: `github.com/mymmrac/telego v1.10.0`

### Completion Notes List

- **Telegram adapter (`transport/telegram`).** New edge actor mirroring `transport/cli`: a bus client that long-polls Telegram, publishes `KindInboundMessage` envelopes, and renders `KindOutboundMessage` replies back to the originating chat. Runs as a supervised `Serve(ctx) error` edge (AD-5). telego is touched only here (an edge); the core fence keeps it out of `core/`.
- **AC1 — end-to-end round-trip + edge mapping.** `TestEndToEndRoundTrip` wires the real bus + arbiter (`worker.Stub{}`) + dispatch + state with a fake `Client`; an inbound `Update{ChatID:42, Text:"hello"}` round-trips to a `Send(42,"hello")` (stub echo) — proving both that the spine delivers and that the native chat id maps to `ConvoID "42"` inbound and reverses to chat `42` outbound. `TestEdgeMapsChatIDToConvoID` asserts the mapping directly. No telego type crosses into core (the fake uses only the adapter-local `Update`).
- **AC2 — swap stays at the edge.** Transport is selected in `main` via `SHELLDON_TRANSPORT` (`cli` default | `telegram`); only the chosen adapter registers on the point-to-point outbound route. No `core/` code changed — `TestCoreDoesNotImportTransport` still passes and telego appears only under `transport/telegram`. `TestServeIsWireableAsEdge` proves `Serve` is accepted by `supervisor.Guard` so the wiring can't silently drift.
- **AC3 — NAT-idle watchdog.** The long-poll `GetUpdatesParams.Timeout` is `longPollTimeout = 30s`, kept under `natIdleWindow = 60s`; `TestNATWatchdogTimeoutUnderWindow` fails the suite if a future bump lets the timeout meet/exceed the window.
- **Credential boundary (AD-12/AD-9/NFR8).** The bot token is the adapter's own surface credential, resolved from `SHELLDON_TELEGRAM_TOKEN` inside `NewFromEnv` — never a broker model/tool cred, never on the bus, never logged. A missing/invalid token degrades to reflex-only under supervision (`main` wires a Serve that returns the error) rather than crashing core.
- **Owner guard (minimal, single-owner M1).** Optional `SHELLDON_TELEGRAM_OWNER_ID` drops inbound updates from non-owner chats (`TestOwnerGuardDropsNonOwner`); unset = accept any chat. No `chat_id`/`user_id` columns (deferred to Epic 4 per AD-7).
- **Design choices vs. story spec.** (1) The narrow client seam is **exported** (`Client`/`Update`) rather than unexported, matching the broker `Provider` / monolith `Completer` pattern and enabling a black-box test package. (2) NAT consts are surfaced to the black-box test via `export_test.go` (mirrors `broker/export_test.go`). (3) The cosmetic `Dst: "cli"` header in `dispatch.publishReply` was left unchanged — the hub routes by `Kind`, so it's correct for either transport. Dispatch, the bus, contracts, the arbiter, the worker, and the broker are all unchanged.

### File List

- `transport/telegram/telegram.go` (new)
- `transport/telegram/telegram_test.go` (new)
- `transport/telegram/export_test.go` (new — exposes NAT consts for the AC3 test)
- `cmd/shelldon/main.go` (modified — transport-selection branch, imports, package + start-order docs)
- `go.mod` / `go.sum` (modified — add `github.com/mymmrac/telego v1.10.0` + transitive deps)
- `_bmad-output/implementation-artifacts/3-4-telegram-adapter-second-transport.md` (story tracking)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (status → review)

### Review Findings

- [ ] [Review][Patch] `transportServe` degradation stub returns error immediately → suture restart loop [cmd/shelldon/main.go]
- [x] [Review][Defer] `Send` is synchronous in Serve's main select → can block outbound drain during slow Telegram API [transport/telegram/telegram.go] — deferred, pre-existing design pattern consistent with CLI adapter; M1 single-owner doesn't produce backpressure
- [x] [Review][Defer] `WithTimeout` on `UpdatesViaLongPolling` may be overridden by telego internals — AC3 test validates constant relationship only, not wire behavior [transport/telegram/telegram.go:170] — deferred, requires telego source audit + live integration test to verify
- [x] [Review][Defer] ConvoID encoded as bare decimal string — no transport prefix, future multi-transport could collide [transport/telegram/telegram.go:156] — deferred, single-transport-at-a-time is the M1 invariant; prefix deferred to Epic 4 multi-transport work
- [x] [Review][Defer] `NewFromEnv` invalid-owner-ID error path untested [transport/telegram/telegram.go:94] — deferred, coverage gap not required by ACs; low risk

## Change Log

- 2026-06-22: Implemented the Telegram chat-transport adapter (`transport/telegram`, telego v1.10.0) as a supervised edge actor and made the transport selectable in `main` (`SHELLDON_TRANSPORT`, CLI default). Owner messages round-trip CLI↔Telegram with no `core/` change (AD-12), the adapter holds its own bot-token credential, and a NAT-idle watchdog keeps the long-poll alive. All 3 ACs satisfied; status → review.
