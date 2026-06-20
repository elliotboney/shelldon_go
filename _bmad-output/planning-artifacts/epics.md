---
stepsCompleted: [1, 2, 3, 4]
inputDocuments:
  - ../specs/spec-shelldon-go/SPEC.md
  - planning-artifacts/architecture/architecture-shelldon_go-2026-06-19/ARCHITECTURE-SPINE.md
---

# shelldon (Go rewrite) - Epic Breakdown

## Overview

This document provides the complete epic and story breakdown for shelldon (Go rewrite), decomposing the requirements from the SPEC (capability contract, in place of a PRD) and the Architecture Spine into implementable stories. Epics follow the milestone arc M0–M4 the architecture reasoned through; capabilities CAP-1…CAP-11 map across them.

## Requirements Inventory

### Functional Requirements

FR1 (CAP-1): The owner sends a text message over the pluggable chat transport; a per-turn brain (remote LLM) replies in the same conversation while the E-Ink display reflects the pet's face/state — demonstrable end-to-end within tolerable latency.
FR2 (CAP-2): The pet feels alive between LLM turns via resident reflexes (blink, idle, time-of-day mood) reading a persistent mood/energy/last-interaction struct, independent of the brain — visibly changing state on a schedule even with the network down.
FR3 (CAP-3): OPTIONAL physical sensing (PiSugar2 button, BLE presence of paired devices) via the plugin model produces observable pet reactions when enabled; with it absent the chat-bot pet still functions fully.
FR4 (CAP-4): The pet acts proactively — initiates behavior (greeting on presence, mood-driven idle) with no preceding user input, driven by personality state and environment.
FR5 (CAP-5): All credential access and the LLM call itself pass through a single capability broker that abstracts provider choice; calling the model or reading creds from outside the broker is impossible by construction, and swapping provider touches only the broker.
FR6 (CAP-6): Context persists across ephemeral turns via hybrid memory — sqlite conversation-history (ordered messages + FTS5 recall) + a `learnings` table, plus a curated markdown layer (about.md, facts/, people/, broker-gated vault/) and an owner-only authoritative DIRECTIVE.md. A prior-turn message is recallable by order and keyword; a curated fact influences a later turn.
FR7 (CAP-7): Extensions are added under ONE plugin model (hardware AND behavioral) — emit events, subscribe to broadcast kinds, own private state, claim a display region — without changing `core/` and with LLM-free-core enforcement still passing.
FR8 (CAP-8): On LLM call failure (error/timeout/rate-limit) the broker retries and/or falls back to the next provider so the turn completes; if all providers fail the pet degrades to reflex-only.
FR9 (CAP-9): The chat conversation runs over a pluggable transport behind a transport-agnostic message contract (Telegram never hardcoded); a second stub adapter can be swapped in by adding an adapter only, no `core/` change.
FR10 (CAP-10): A core-resident scheduler runs named jobs at independent cadences (interval/cron/idle-triggered), cost-tiered (reflex vs turn jobs), bounded by a daily credit/turn budget and battery-aware (stretches cadences / skips non-essential LLM turns on battery).
FR11 (CAP-11): Light self-improving learning — the worker proposes `capture_learning` on the hot path; the dream cycle (a scheduled worker turn) classifies pending learnings and promotes durable/high-value ones into curated markdown (sensitive → broker-gated vault), pruning the rest.

### NonFunctional Requirements

NFR1: 512MB RAM ceiling (Pi Zero 2W) bounds every design choice; nothing accumulates across turns — per-turn memory bounded by Go GC under `GOMEMLIMIT`≈280MiB (not process death). v1's OOM is the defining failure designed out.
NFR2: Single static binary — `CGO_ENABLED=0`, `GOARCH=arm64`; pure-Go deps only (`modernc.org/sqlite` for FTS5, `periph.io`) so cross-compile stays a one-line build.
NFR3: `core/` must remain LLM-free, mechanically enforced — `depguard` (via golangci-lint) + `internal/` packaging fail the build if `core/` imports provider/LLM modules; provider SDKs live behind `broker/internal/`.
NFR4: ≤1 worker turn in flight at all times; events during a turn coalesce per job-class into a single pending catch-up slot (never a growing backlog).
NFR5: The worker (untrusted, prompt-assembling brain) lives behind a swappable isolation seam — goroutine in Monolith+ (M0–M2), uid-separated subprocess in Privsep-lite (M3+) — without reshaping any caller.
NFR6: The secret `vault/` does not EXIST until the worker is across a process/uid wall (M3); before then nothing a goroutine-worker could read. Vault isolation is OS-enforced once active.
NFR7: Core is the sole WRITER of all state and memory (incl. sqlite); workers only READ history and non-vault markdown and propose writes via `Result`.
NFR8: Credential split — the transport adapter holds its own connection credential; the broker is the sole holder of MODEL+TOOL creds and sole egress. No credentials ever cross the bus.
NFR9: Typed, versioned contracts (`Envelope`/`Job`/`Result`, Go structs) with a test harness from M0. Required M0 tests: contract gob round-trip, ≤1-worker bound, atomic-write crash-safety, and soul-survives-a-single-edge-panic.
NFR10: The soul survives ANY single edge failure — every edge is a suture/v4 supervised Service with its own recover()+backoff; a dead edge degrades gracefully (transport down→reflex-only; providers exhausted→reflex fallback; plugin crash→kills its widget); systemd `Restart=always`+`OOMPolicy=stop` is the outer net.
NFR11: SD-card write wear — high-frequency state stays in RAM checkpointed to one file; sqlite uses WAL + batched commits; markdown writes are atomic (temp + fsync + rename + parent-dir fsync via renameio/v2). No vector DB.
NFR12: E-Ink refresh latency is in seconds, not frames; behaviors and animations must tolerate it (region compositor, monotonic seq, latest-wins, drop stale frames).
NFR13: Remote-LLM network dependency — no brain when offline (incl. Ollama-LAN); the pet must degrade gracefully to resident reflexes.
NFR14: Battery + credit-aware autonomy — no unbounded background LLM spend; background turn jobs cooldown-gated and bounded by a daily credit/turn budget; scheduler backs off on battery.
NFR15: BLE presence is pair-first (only previously-paired devices, keyed on stable BLE address); arbitrary nearby devices are never scanned or logged.
NFR16: Observability — structured logging via `log/slog` → journald (Type=simple), persistent with size caps; panics, edge restarts, turn lifecycle, and provider fallbacks are logged events.

### Additional Requirements

(from ARCHITECTURE-SPINE.md AD-1…AD-17 — the milestone arc and technical scaffolding)

- **Milestone arc (epic backbone):** M0 walking skeleton → M1 brain (broker + provider chain + real worker) → M2 soul (reflexes + scheduler + arbiter) → M3 memory + Privsep-lite isolation activation → M4 face & body (display compositor + plugins + real hardware).
- **M0 is a real walking skeleton** — end-to-end through the real channel bus, with `contracts/` + minimal hub + one transport + a fake worker, building `CGO_ENABLED=0` and passing the four required tests ON THE PI, not just the laptop.
- **Worker seam contract:** the worker is a Go interface `Worker.AssembleAndPropose(ctx, turn) (Result, error)`; Monolith+ goroutine implementation ships first, Privsep-lite subprocess swaps in at M3 behind the unchanged seam (transport channel→UDS+gob).
- **Bus contract:** uniform `Envelope`/`Job`/`Result` Go structs over a core-owned in-process channel hub; closed header `id/v/kind/src/dst/turn_id`; two routing modes (point-to-point + broadcast subscription from plugin manifests); each event kind co-versioned with a payload struct in `contracts/`.
- **Supervision:** suture/v4 supervisor tree (core = root); graceful shutdown via `signal.NotifyContext` draining in reverse start order.
- **Provider chain:** failsafe-go (retry + breaker + timeout + fallback); GLM default via go-openai base-URL swap; Ollama-LAN/OpenAI/OpenRouter/Anthropic alternates; anthropics/anthropic-sdk-go streaming.
- **Memory layer:** modernc.org/sqlite (WAL + FTS5) + renameio/v2 atomic markdown; core single-writer applies proposed memory-ops serially (no race on learnings pattern_key).
- **Display:** dedicated goroutine owning the panel (periph.io Draw blocks on busy pin); size-1 drain-replace channel; full-refresh every 5–10 partials, ≥1/24h.
- **Plugin registry:** compile-time registration in `main`; Go-struct manifest; conflicting GPIO/region claims rejected at startup.
- **Deploy:** Pi OS 64-bit + systemd (MemoryHigh=180M < MemoryMax=220M, OOMPolicy=stop, Restart=always; do NOT set PrivateDevices); cross-compile on laptop, rsync atomic-swap, never compile on the Pi; gokrazy deferred.
- **Testing:** testing/synctest for deterministic scheduler cadences; narrow interfaces over SPI/GPIO/LLM/clock injected at main (no monkeypatch); `go test -race` on CI/laptop only.

### UX Design Requirements

(none — no separate UX design contract; the pet's E-Ink face/display is specified in the architecture spine, AD-6 region compositor, and covered by FR1/FR2/NFR12)

### FR Coverage Map

- FR1 (chat-turn brain+face): Epic 3 (real LLM reply) — terminal face in Epic 2, E-Ink face in Epic 6
- FR2 (resident reflexes): Epic 2
- FR3 (optional physical sensing): Epic 6
- FR4 (proactive action): Epic 2 (proactive mechanism + reflex-driven) — LLM-driven pings in Epic 3
- FR5 (capability broker): Epic 3
- FR6 (hybrid memory): Epic 4
- FR7 (plugin model): Epic 6
- FR8 (LLM fallback): Epic 3
- FR9 (pluggable transport): Epic 3 (Telegram 2nd adapter proves pluggability) — CLI 1st adapter in Epic 1
- FR10 (autonomous scheduler): Epic 2 (reflex-tier) — turn-tier in Epic 3
- FR11 (self-improving dream cycle): Epic 4 (non-sensitive) — sensitive-classification lane in Epic 5

## Epic List

### Epic 1 (M0): Tracer Bullet
Stand up `contracts/` + the core-owned channel bus + the suture supervisor root + the `Worker` seam (stub implementation), and prove the spine end-to-end: a CLI message round-trips through the worker seam to a stub and back, with the four required tests green on the Pi. Foundational risk-boundary epic — honest naming, no pet language.
**Success criterion:** the four required M0 tests (contract gob round-trip, ≤1-worker-in-flight, atomic-write crash-safety, soul-survives-a-single-edge-panic) pass on the Pi, AND a CLI message round-trips through the real bus and the worker seam to a stub and back.
**FRs covered:** FR9 (CLI, foundational) · **NFRs:** NFR2, NFR9, NFR10

### Epic 2 (M1): The Soul
Resident reflexes (blink, idle, mood drift), the reflex-tier scheduler, the personality-state struct with periodic checkpoint, the terminal (ANSI) face behind the region-compositor contract, and offline acknowledgement. Free, offline, zero LLM credit. Lands an "alive" demo before the brain exists.
**FRs covered:** FR2, FR4 (proactive mechanism + reflex-driven), FR10 (reflex-tier)
**Conditions:** scheduler is one tier-shaped component (reflex-tier here, turn-tier in Epic 3, no core-loop refactor between); the terminal face sits behind the same compositor contract the E-Ink renderer will use (no special-casing in core).

### Epic 3 (M1): The Brain
The capability broker (sole cred holder, depguard-fenced), the ordered provider chain with retry/fallback (failsafe-go; GLM default), the real worker behind the seam, the Telegram adapter (second transport, proving pluggability), the turn-tier scheduler, LLM-driven proactive pings, and graceful fallback to reflex when the provider chain is exhausted.
**FRs covered:** FR1, FR5, FR8, FR9, FR4 (LLM-driven pings), FR10 (turn-tier)

### Epic 4 (M2): Memory & Dreams
Hybrid memory — modernc.org/sqlite (WAL + FTS5) conversation history + learnings, plus the atomic markdown curated tree (renameio/v2) — with hot-path `capture_learning` and the dream cycle that consolidates and promotes durable learnings. Still Monolith+; the sensitive-classification lane stays OFF (no vault exists yet).
**FRs covered:** FR6, FR11 (non-sensitive promotion only)
**Conditions:** core applies proposed memory-ops serially (single writer, no learnings pattern_key race); the dream cycle does NOT classify anything as sensitive until Epic 5 lights up the vault (a tested, gated flag).

### Epic 5 (M3): The Wall
Activate Privsep-lite — the worker becomes a uid-separated recycled subprocess behind the unchanged seam, the transport under it swaps to UDS+gob, the vault comes into existence (OS-unreadable to the worker uid), and the dream cycle's sensitive-classification lane turns on. Gated by the explicit threat-model confirmation and a vault-isolation property test.
**FRs covered:** FR11 (sensitive-classification completion) · **NFRs:** NFR5, NFR6 activation
**Conditions:** the channel→gob transport swap reshapes no caller (proven by the M0 gob round-trip suite running green against both transports); the threat-model confirmation + vault-isolation property test are the gate to merge.

### Epic 6 (M4): Face & Body
Swap the render target from terminal to the Waveshare E-Ink compositor (same region contract), stand up the compile-time plugin registry, and bring up PiSugar2 (power + button) and BLE presence on real hardware.
**FRs covered:** FR3, FR7, FR1 (E-Ink face completion)
**Conditions:** the E-Ink renderer implements the same compositor contract as the terminal face (Epic 2); conflicting GPIO/display-region claims are rejected at startup.

## Epic 1 (M0): Tracer Bullet

**Epic goal:** Stand up `contracts/` + the core-owned channel bus + the suture supervisor root + the `Worker` seam (stub), and prove the spine end-to-end — a CLI message round-trips through the worker seam to a stub and back — with the four required M0 tests green on the Pi. Foundational risk-boundary epic; no pet behavior, no LLM.

### Story 1.1: Versioned contracts + gob round-trip

As a developer building shelldon,
I want versioned `Envelope`/`Job`/`Result` Go structs with the closed header in `contracts/` that round-trip through gob,
So that the M3 UDS+gob transport swap cannot surface a serialization incompatibility later and the contract stays a binding invariant.

**Acceptance Criteria:**

**Given** the `Envelope`/`Job`/`Result` structs with the closed header (`id/v/kind/src/dst/turn_id`) defined in `contracts/`
**When** every `Envelope` kind is gob-encoded and decoded in the required M0 round-trip test (NFR9, AD-10)
**Then** each decoded value is equal to the original
**And** the test covers every declared envelope kind, not a representative sample.

**Given** a contract value carrying a `v` version field
**When** an additive field is appended to a payload struct
**Then** a decoder built against the older struct still decodes the value without error (additive-only, NFR9/AD-10).

**Given** the contracts package compiled
**When** `depguard` runs over `contracts/`
**Then** no provider/LLM SDK import is present and the package builds `CGO_ENABLED=0`.

### Story 1.2: Core-owned channel hub + point-to-point routing

As a developer building shelldon,
I want a core-owned in-process channel hub that routes a `Job` to its registered destination by `kind`,
So that edges communicate only through the bus and an unknown destination fails safe instead of crashing the soul (AD-1, AD-4).

**Acceptance Criteria:**

**Given** a destination registered for a given `kind` in the point-to-point routing table
**When** a `Job` addressed by that `kind` is published to the hub
**Then** it is delivered to exactly that registered destination.

**Given** no destination registered for a `kind`
**When** a `Job` addressed by that `kind` is published
**Then** the hub returns a routing error and never panics (AD-4).

**Given** any envelope traversing the hub
**When** its contents are inspected
**Then** no credential field is present on the bus (NFR8 — no creds ever on the bus).

### Story 1.3: Worker seam interface + stub + ≤1-in-flight arbiter gate

As a developer building shelldon,
I want the `Worker.AssembleAndPropose(ctx, turn) (Result, error)` interface with a stub implementation and an arbiter that admits at most one worker turn,
So that the isolation seam and the ≤1-worker invariant exist from M0 and never reshape callers when the real worker or subprocess swaps in (AD-2, AD-8, NFR4).

**Acceptance Criteria:**

**Given** the `Worker` interface and a stub implementation behind it
**When** `AssembleAndPropose` is called for a turn
**Then** the stub returns a well-formed `Result` with no error.

**Given** one worker turn already in flight
**When** a second turn is submitted to the arbiter (the required ≤1-worker M0 test, NFR4/AD-8)
**Then** the second turn is coalesced or rejected and the two turns never run concurrently.

**Given** the arbiter gate under `go test -race`
**When** the ≤1-worker test runs
**Then** no data race is reported.

### Story 1.4: suture supervisor root + soul-survives-edge-panic

As a developer building shelldon,
I want `core` as the suture/v4 supervisor root with every edge a supervised `Service` carrying its own `recover()` + backoff and graceful shutdown,
So that a single edge panic degrades gracefully instead of killing the pet (the required soul-survives M0 test, AD-5, NFR10).

**Acceptance Criteria:**

**Given** core supervising multiple edge `Service`s
**When** a panic is injected into one edge `Service` (the required soul-survives-a-single-edge-panic test, NFR10/AD-10)
**Then** core and every other edge keep running and the panicked edge is restarted with backoff.

**Given** each edge `Service`'s `Serve(ctx) error`
**When** its implementation is inspected
**Then** a `defer recover()` is present per edge (recover() does not cross goroutines, AD-5).

**Given** a running process
**When** a shutdown signal arrives via `signal.NotifyContext`
**Then** edges drain in reverse start order and the process exits cleanly (AD-5).

### Story 1.5: CLI transport adapter + end-to-end round-trip

As a developer building shelldon,
I want a CLI chat-transport adapter that emits inbound-message and consumes outbound-message envelopes over the transport-agnostic contract,
So that a message round-trips inbound→core→worker seam→stub→outbound→CLI and proves the spine is wired end-to-end (FR9, AD-12).

**Acceptance Criteria:**

**Given** the CLI adapter wired to the bus as an edge actor
**When** a CLI message is entered
**Then** it round-trips inbound→core→worker seam→stub→outbound and the reply renders back at the CLI.

**Given** the CLI adapter and core compiled together
**When** core's imports are inspected
**Then** no CLI- or telego-specific type leaks into core — core sees only the transport-agnostic message contract in `contracts/` (AD-12).

### Story 1.6: Cross-compile + atomic-write crash-safety + on-Pi run

As a developer building shelldon,
I want the spine to cross-compile to a single static arm64 binary, perform crash-safe atomic markdown writes, and pass all four required tests plus the CLI round-trip on the Pi itself,
So that M0 is a real walking skeleton validated on target hardware, not just the laptop (NFR2, AD-10).

**Acceptance Criteria:**

**Given** the M0 build
**When** `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build` runs
**Then** it produces a single static binary with no CGo dependency (NFR2).

**Given** a markdown write via renameio/v2
**When** the write is interrupted mid-rename (the required atomic-write crash-safety test, NFR11/AD-7/AD-10)
**Then** the prior file is left intact — no partial/corrupt file is observable.

**Given** the binary deployed to the Pi Zero 2W
**When** the four required M0 tests run ON THE PI (gob round-trip, ≤1-worker, atomic-write crash-safety, soul-survives-edge-panic)
**Then** all four pass on the Pi, not only the laptop (AD-10).

**Given** the binary running on the Pi
**When** a CLI message is sent
**Then** the inbound→core→worker seam→stub→outbound round-trip completes on the Pi.

## Epic 2 (M1): The Soul

**Epic goal:** Resident reflexes (blink, idle, mood drift), the reflex-tier scheduler, the personality-state struct with periodic checkpoint, the terminal (ANSI) face behind the region-compositor contract, and offline acknowledgement — free, offline, zero LLM credit. Lands an "alive" demo before the brain exists.

### Story 2.1: Personality-state struct + periodic checkpoint

As Elliot (the owner),
I want the pet's mood/energy/last-interaction state to live in RAM and checkpoint to one small file on a cadence,
So that the pet has continuity across restarts without wearing the SD card (FR2, AD-16, NFR11).

**Acceptance Criteria:**

**Given** the personality-state struct (mood/energy/last-interaction) held in RAM
**When** the checkpoint cadence elapses (verifiable under testing/synctest with the fake clock)
**Then** the state is written to exactly one checkpoint file (AD-16/NFR11).

**Given** a checkpoint file on disk
**When** the process restarts
**Then** personality-state is restored from the checkpoint, not reset to defaults.

**Given** RAM is the working copy
**When** any durable layer (markdown/sqlite) is read
**Then** RAM state is never treated as the source of truth for those layers (AD-16).

### Story 2.2: Region-compositor contract + terminal (ANSI) face

As Elliot (the owner),
I want core to push face-region snapshots with a monotonic seq and a terminal renderer that renders latest-wins,
So that the pet has a visible face through the same compositor seam the E-Ink renderer will later use, with no terminal code in core (FR2, AD-6).

**Acceptance Criteria:**

**Given** core pushing face-region snapshots over the compositor contract with a monotonic `seq`
**When** snapshots arrive at the terminal renderer
**Then** the renderer renders the latest snapshot and drops any frame with a stale `seq` (AD-6).

**Given** core and the terminal renderer compiled together
**When** core's imports are inspected
**Then** core contains no terminal-specific code — the region-compositor contract is the only seam (AD-6).

**Given** the region-id type
**When** the compositor and renderer compile
**Then** both reference the single closed region-id enum in `contracts/` (AD-6).

### Story 2.3: Blink + idle reflexes

As Elliot (the owner),
I want the pet to blink at jittered intervals and react when idle,
So that it visibly feels alive between turns even with the network down (FR2, de-vibed AC1).

**Acceptance Criteria:**

**Given** no inbound message for the idle threshold (verifiable under testing/synctest with the fake clock advanced)
**When** the idle threshold elapses
**Then** a blink frame is rendered.

**Given** repeated blink cycles under the fake clock
**When** inter-blink intervals are measured
**Then** the interval is jittered (not a fixed constant) across cycles.

### Story 2.4: Mood-drift reflex

As Elliot (the owner),
I want the pet's mood to drift over time on a cadence and persist,
So that its personality shifts believably across days without any LLM (FR2, FR4 reflex-driven, de-vibed AC3).

**Acceptance Criteria:**

**Given** the mood-drift cadence (verifiable under testing/synctest with the fake clock advanced a simulated week)
**When** the cadence elapses
**Then** personality-state valence moves by the configured step and is checkpointed (AD-16).

**Given** a simulated week of fake-clock advance
**When** valence is asserted before and after
**Then** the accumulated drift matches the configured per-cadence step times the number of elapsed cadences.

### Story 2.5: Reflex-tier scheduler

As the system,
I want a scheduler that runs named reflex-tier jobs at independent cadences (interval / idle-triggered) with no LLM,
So that mood-drift, blink, and other reflexes each fire on their own schedule from one tier-shaped component (FR10 reflex-tier, AD-13).

**Acceptance Criteria:**

**Given** two reflex jobs registered at independent cadences (verifiable under testing/synctest)
**When** the fake clock advances past both cadences
**Then** each job fires the correct number of times on its own cadence, independently.

**Given** any reflex-tier job
**When** it executes
**Then** it runs in-core with no worker invocation and no LLM call (AD-13 cost tier).

> NOTE: this is the reflex tier of the ONE tier-shaped scheduler. The turn tier is added in Epic 3 (Story 3.5) with NO core-loop refactor — Yui's condition.

### Story 2.6: Offline acknowledgement (brainless-alive)

As Elliot (the owner),
I want the pet to acknowledge a message even with no brain available, without ever hanging,
So that it stays responsive and alive when offline (FR2, NFR13, de-vibed AC2).

**Acceptance Criteria:**

**Given** no brain/worker available
**When** an inbound message arrives
**Then** a reflex acknowledgement is produced and the inbound path never blocks (NFR13).

**Given** a turn that cannot complete because the brain is absent
**When** the arbiter timeout elapses
**Then** no turn remains in-flight past the timeout — it is closed and the pet degrades to reflex (AD-8/AD-11).

## Epic 3 (M1): The Brain

**Epic goal:** The capability broker (sole cred holder, depguard-fenced), the ordered provider chain with retry/fallback (failsafe-go; GLM default), the real worker behind the seam, the Telegram adapter (second transport, proving pluggability), the turn-tier scheduler, LLM-driven proactive pings, and graceful fallback to reflex when the provider chain is exhausted.

### Story 3.1: Capability broker + credential boundary

As the system,
I want model/tool credentials held only inside the broker and a build that fails if anything outside `broker/internal/` imports a provider SDK,
So that a prompt-injected worker can never reach secrets or call models directly (FR5, NFR3, NFR8, AD-9).

**Acceptance Criteria:**

**Given** the broker holding model/tool creds
**When** the broker exposes a client to callers
**Then** it exposes only a pre-authorized client (an auth-injecting RoundTripper), never the raw key (NFR8/AD-9).

**Given** an import of a provider/LLM SDK added to `core/` or any package outside `broker/internal/`
**When** `depguard` runs in the build
**Then** the build fails (NFR3/AD-9).

**Given** any `Job` envelope leaving the bus
**When** its fields are inspected
**Then** it carries no credentials — the broker injects them internally (NFR8).

### Story 3.2: Provider chain with retry/fallback

As Elliot (the owner),
I want an ordered provider chain (GLM default via base-URL swap) with retry and fallback,
So that a turn still completes when a provider errors, and the pet degrades to reflex only if all providers fail (FR8, AD-8, AD-9).

**Acceptance Criteria:**

**Given** the failsafe-go provider chain with at least two providers
**When** the first provider returns a 500/timeout (injected fault)
**Then** the turn completes via the next provider in the chain (FR8/AD-9).

**Given** every provider in the chain failing
**When** the turn is attempted
**Then** the arbiter degrades to a reflex behavior and the pet never freezes (AD-8/AD-9).

**Given** the default provider configuration
**When** the broker initializes
**Then** GLM is selected as default via a go-openai base-URL swap (AD-9).

### Story 3.3: Real worker behind the seam

As Elliot (the owner),
I want a real LLM-backed worker behind the unchanged `Worker` seam that assembles prompts, streams replies, and proposes memory-ops,
So that owner messages get real conversational replies while core stays the sole writer (FR1, AD-2, AD-6, AD-11).

**Acceptance Criteria:**

**Given** the real worker behind the seam
**When** an owner message is processed
**Then** an LLM reply is produced in the same conversation (FR1) and any memory change is carried as a proposed memory-op in `Result`, never written directly by the worker (AD-6).

**Given** an in-flight turn
**When** the turn's `context` is cancelled (timeout/superseded)
**Then** the in-flight LLM turn is killed and a late `Result` for a closed `turn_id` is discarded (AD-11).

### Story 3.4: Telegram adapter (second transport)

As Elliot (the owner),
I want a Telegram chat-transport adapter that carries owner messages and replies end-to-end,
So that swapping CLI↔Telegram proves the transport is pluggable with no core change (FR9, AD-12).

**Acceptance Criteria:**

**Given** the Telegram adapter wired as an edge actor
**When** an owner sends a Telegram message
**Then** it round-trips through core to a reply end-to-end over Telegram.

**Given** the CLI adapter swapped for the Telegram adapter
**When** the swap is made
**Then** no `core/` code changes — only the adapter is added/selected (FR9/AD-12).

**Given** the Telegram long-poll running
**When** the connection idles past the NAT window
**Then** a NAT-idle watchdog keeps the long-poll alive with `Timeout` under the NAT window (AD-12).

### Story 3.5: Turn-tier scheduler + budget/battery gate

As the system,
I want turn-tier jobs (proactive, dreaming) that are cooldown-gated and bounded by a daily credit/turn budget, added to the existing scheduler,
So that background LLM spend is bounded and the turn tier is added without refactoring the reflex-tier loop (FR10 turn-tier, NFR14, AD-8, AD-13).

**Acceptance Criteria:**

**Given** turn-tier jobs registered alongside the reflex tier (tested with a fake clock + fake provider so no real credit burns)
**When** a turn job is due but the daily credit/turn budget is exhausted or its cooldown has not elapsed
**Then** the job is deferred/skipped and no worker invocation occurs (NFR14/AD-8).

**Given** the existing reflex-tier scheduler from Epic 2
**When** the turn tier is added
**Then** the reflex-tier loop is not refactored — the turn tier is layered onto the same tier-shaped scheduler (Yui's condition, AD-13).

### Story 3.6: LLM-driven proactive pings

As Elliot (the owner),
I want the pet to sometimes message me first, gated by cooldown and budget,
So that it acts proactively without spamming or overspending (FR4, AD-8).

**Acceptance Criteria:**

**Given** no preceding owner input
**When** a proactive-ping turn job fires within its cooldown and daily budget
**Then** the pet initiates an outbound message (FR4).

**Given** a proactive ping recently sent
**When** another would fire before the minimum-interval cooldown elapses
**Then** it is suppressed by the cooldown gate (AD-8).

## Epic 4 (M2): Memory & Dreams

**Epic goal:** Hybrid memory — modernc.org/sqlite (WAL + FTS5) conversation history + learnings, plus the atomic markdown curated tree (renameio/v2) — with hot-path `capture_learning` and the dream cycle. Still Monolith+; the sensitive-classification lane stays OFF (no vault exists yet).

> NOTE: Epic 4 stories are lighter stubs — they will be re-detailed at build time via bmad-create-story.

### Story 4.1: sqlite conversation store

As Elliot (the owner),
I want conversation history stored in pure-Go sqlite with WAL and FTS5,
So that prior turns are recallable by order and by keyword across ephemeral turns (FR6, AD-7, NFR2).

**Acceptance Criteria:**

**Given** the modernc.org/sqlite store (WAL + FTS5) built `CGO_ENABLED=0`
**When** a prior-turn message is queried by recency order and by FTS5 keyword
**Then** the message is recalled by both order and keyword (FR6/AD-7).

### Story 4.2: learnings table + hot-path capture_learning

As the system,
I want the worker to propose `capture_learning` on the hot path and core to apply it serially as single writer,
So that learnings dedup on `pattern_key` with no row race (FR11, AD-6, AD-7).

**Acceptance Criteria:**

**Given** the worker proposing `capture_learning(observation, pattern_key?)` in `Result`
**When** core applies proposals serially as the single writer
**Then** a `pattern_key` match increments `recurrence_count` with no race (the reviewer-gate dedup fix, AD-6/AD-7).

### Story 4.3: curated markdown tree + DIRECTIVE.md

As Elliot (the owner),
I want an atomic curated markdown tree and an owner-only authoritative DIRECTIVE.md injected into every prompt,
So that durable knowledge survives crashes and the owner's constitution is never overwritten by the bot (FR6, AD-7, NFR11).

**Acceptance Criteria:**

**Given** the curated markdown tree
**When** any markdown write occurs
**Then** it is atomic via renameio/v2 (NFR11/AD-7).

**Given** DIRECTIVE.md present
**When** a prompt is assembled
**Then** DIRECTIVE.md is read in as authoritative and is never a memory-op target — disjoint writer sets (AD-7).

### Story 4.4: dream cycle (non-sensitive)

As the system,
I want a scheduled dream turn that promotes recurring learnings into curated markdown, with the sensitive lane off,
So that durable learnings influence later turns while no vault-routing happens before Epic 5 (FR11, AD-15).

**Acceptance Criteria:**

**Given** a recurring `pending` learning
**When** a dream turn runs
**Then** it is promoted to curated markdown and influences a later turn (FR11/AD-15).

**Given** the sensitive-classification lane
**When** the dream cycle runs (no vault exists yet — Boundary's hole)
**Then** the sensitive gating flag is OFF and tested as off — nothing is routed to a vault.

## Epic 5 (M3): The Wall

**Epic goal:** Activate Privsep-lite — the worker becomes a uid-separated recycled subprocess behind the unchanged seam, the transport under it swaps to UDS+gob, the vault comes into existence (OS-unreadable to the worker uid), and the dream cycle's sensitive-classification lane turns on. Gated by an explicit threat-model confirmation and a vault-isolation property test.

> NOTE: Epic 5 stories are lighter stubs — they will be re-detailed at build time via bmad-create-story.

### Story 5.1: Privsep-lite worker subprocess + gob transport swap

As the system,
I want the worker to run as a uid-separated recycled subprocess behind the unchanged seam, with the transport swapped to gob/UDS,
So that the isolation hardening is invisible to callers (NFR5, AD-2, AD-4).

**Acceptance Criteria:**

**Given** the worker re-exec'd as a uid-separated recycled subprocess (`/proc/self/exe`) behind the unchanged `Worker` seam
**When** the M0 contract round-trip suite runs against BOTH the channel and the gob/UDS transport
**Then** both pass green, proving the transport swap reshapes no caller (Murat's test, AD-2/AD-4).

### Story 5.2: Vault + isolation property test

As the system,
I want the vault created with permissions excluding the worker uid and a property test proving the worker cannot read it,
So that vault isolation is OS-enforced, not a path filter (NFR6, AD-3).

**Acceptance Criteria:**

**Given** the vault directory created with permissions excluding the worker uid
**When** a property test attempts to read vault contents from a worker process
**Then** the read is denied — the worker process cannot read the vault (NFR6/AD-3).

### Story 5.3: Sensitive-classification lane on

As the system,
I want the dream cycle to route sensitive promotions to the broker-gated vault now that the vault is live,
So that sensitive learnings are durably stored behind the wall (FR11, AD-9, AD-15).

**Acceptance Criteria:**

**Given** the live vault and the worker across the process wall
**When** the dream cycle classifies a learning as sensitive
**Then** the promotion is routed to the broker-gated vault (AD-9/AD-15).

**Given** the sensitive-lane merge
**When** it is gated for release
**Then** it is blocked on the explicit threat-model confirmation plus the passing 5.2 isolation test (the deferred M3 gate).

## Epic 6 (M4): Face & Body

**Epic goal:** Swap the render target from terminal to the Waveshare E-Ink compositor (same region contract), stand up the compile-time plugin registry, and bring up PiSugar2 (power + button) and BLE presence on real hardware.

> NOTE: Epic 6 stories are lighter stubs — they will be re-detailed at build time via bmad-create-story.

### Story 6.1: E-Ink compositor renderer

As Elliot (the owner),
I want the Waveshare E-Ink renderer to implement the same region-compositor contract as the terminal face, selected by config,
So that the render target swaps with no core change (FR1 E-Ink completion, AD-6, NFR12).

**Acceptance Criteria:**

**Given** the Waveshare renderer (periph.io) implementing the same region-compositor contract as the terminal face (Epic 2)
**When** the render target is selected by config
**Then** the E-Ink face renders with no core change (the render-target swap, AD-6).

**Given** the E-Ink panel under partial refreshes
**When** refresh cadence is measured
**Then** a full refresh occurs every 5–10 partials and at least once per 24h (AD-6/NFR12).

### Story 6.2: Compile-time plugin registry

As a developer building shelldon,
I want plugins added by compile-time registration with conflicting claims rejected at startup,
So that hardware and behavioral plugins extend the pet without touching core and without breaking LLM-free-core enforcement (FR7, AD-14).

**Acceptance Criteria:**

**Given** a plugin (hardware or behavioral) registered at compile time in `main`
**When** the build runs
**Then** core is unchanged and the build still passes LLM-free-core enforcement (FR7/AD-14).

**Given** two plugins claiming the same GPIO pin or display region
**When** the process starts
**Then** the conflicting claim is rejected at startup (AD-14).

### Story 6.3: PiSugar2 plugin + battery-aware scheduler

As Elliot (the owner),
I want a PiSugar2 plugin that reacts to button presses and a scheduler that backs off on battery,
So that the pet responds to physical input and stops burning LLM credit when unplugged (FR3, NFR14, AD-13).

**Acceptance Criteria:**

**Given** the PiSugar2 plugin enabled
**When** the button is pressed
**Then** an observable pet reaction occurs (FR3).

**Given** the scheduler reading PiSugar2 power state
**When** the device is on battery / low charge
**Then** non-essential LLM turns are stretched or skipped (NFR14/AD-13).

### Story 6.4: BLE presence plugin (pair-first)

As Elliot (the owner),
I want a BLE presence plugin that reacts only to previously-paired devices,
So that the pet greets known devices while never scanning or logging arbitrary nearby devices (FR3, NFR15).

**Acceptance Criteria:**

**Given** the BLE plugin with a previously-paired device
**When** that device comes into presence
**Then** an observable pet reaction occurs (FR3).

**Given** arbitrary unpaired nearby devices
**When** the BLE plugin is running
**Then** they are never scanned or logged — pair-first only (NFR15).
