---
baseline_commit: 7ccd94c
---

# Story 3.2: Provider chain with retry/fallback

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As Elliot (the owner),
I want an ordered provider chain (GLM default via base-URL swap) with retry and fallback,
so that a turn still completes when a provider errors, and the pet degrades to reflex only if all providers fail (FR8, AD-8, AD-9).

## Context

**Second story of Epic 3 (M1 — "The Brain").** Story 3.1 built the credential boundary — the broker holds the model key and exposes an auth-injecting client, behind a depguard fence that forbids provider SDKs outside `broker/internal/`. This story fills that boundary with the **model egress**: an ordered chain of LLM providers composed with **failsafe-go** (retry + timeout + fallback), so a single provider's 500/timeout doesn't fail the turn — the chain falls through to the next provider, and only total exhaustion surfaces an error (which the arbiter already degrades to reflex, Story 2.6).

**The depguard fence goes live here.** 3.1's fence was vacuous (no SDK in `go.mod`). 3.2 adds the **first provider SDK** — `sashabaranov/go-openai` — which **must** live under `broker/internal/`. The `Provider` interface boundary uses broker-local request/response types (no go-openai types leak out of `internal/`), so `broker/` composes the chain without importing the SDK. If go-openai is accidentally imported outside `broker/internal/`, both `golangci-lint run` and `broker/imports_test.go` fail.

**Two new dependencies, both architecture-specified (not a HALT).** `github.com/sashabaranov/go-openai` (GLM/OpenAI/OpenRouter via base-URL swap) and `github.com/failsafe-go/failsafe-go` (retry/breaker/timeout/fallback) are named in the architecture Stack table and this story's ACs — they are *in* scope, so the dev adds them via `go get` without pausing for approval. Both are pure-Go (`CGO_ENABLED=0`-safe).

**Why there's still no live caller.** Like 3.1, the broker has no consumer until the real worker (3.3). 3.2 builds `broker.Complete(ctx, Request) (Response, error)` — the method the worker will call — and tests it in isolation with **fake fault-injecting providers** (AC1/AC2) and a **httptest-backed go-openai provider** (AC3), so no real network or credits are touched. The arbiter's degrade-to-reflex on a returned error already exists (2.6) and connects in 3.3 — 3.2 only proves the chain returns a clean error on exhaustion.

**This story does NOT:**
- build the real worker or wire the broker to any caller (Story 3.3) — `broker.Complete` is built and tested in isolation; `worker.Stub` and the arbiter are unchanged
- change the arbiter or its degrade-to-reflex path (already built in 2.6) — AC2's "degrades to reflex" is the existing path; 3.2 only guarantees the broker returns an error on total exhaustion
- assemble prompts / read memory (Story 3.3) — `Request` carries caller-supplied messages; the worker fills them later
- add tool egress, safety policy, or streaming (later Epic 3+) — 3.2 is non-streaming chat completion over the chain
- wire credentials/config for every alternate provider — GLM is the configured default (AC3); additional providers are config/env-driven and can be added without code change
- touch `core/`, contracts, reflexes, scheduler, or `main.go`

## Acceptance Criteria

1. **Fallback on provider error.**
   **Given** the failsafe-go provider chain with at least two providers
   **When** the first provider returns a 500/timeout (injected fault)
   **Then** the turn completes via the next provider in the chain (FR8/AD-9).

2. **Exhaustion returns a clean error (degrade-to-reflex).**
   **Given** every provider in the chain failing
   **When** a completion is attempted
   **Then** `broker.Complete` returns an error (no panic, no hang) — the error the arbiter already degrades to a reflex behavior on (Story 2.6), so the pet never freezes (AD-8/AD-9).

3. **GLM default via base-URL swap.**
   **Given** the default provider configuration
   **When** the broker initializes
   **Then** the default provider is a go-openai client with GLM's base URL (a base-URL swap on the OpenAI-compatible client), using the broker's pre-authorized transport (AD-9).

## Tasks / Subtasks

- [x] **Task 0 — Add dependencies** (AC: 1, 3)
  - [x] Added `github.com/sashabaranov/go-openai v1.41.2` + `github.com/failsafe-go/failsafe-go v0.9.6` (+ one indirect: `bits-and-blooms/bitset`). `go mod tidy`. Both pure-Go; native + arm64 `CGO_ENABLED=0` build.
  - [x] **Verified the failsafe-go v0.9.6 API from the module cache** (it is generics-based): `failsafe.With[R](policies...).WithContext(ctx).Get(fn)`, `retrypolicy.NewBuilder[R]().WithMaxRetries(n).WithDelay(d).Build()`, `timeout.New[R](d)`.

- [x] **Task 1 — Provider interface + neutral request/response types (`broker/provider.go`)** (AC: 1, 2)
  - [x] Broker-local `Message{Role,Content}`, `Request{Model,Messages}`, `Response{Text}` — SDK-free.
  - [x] `Provider interface { Name() string; Complete(ctx, Request) (Response, error) }`. Chain is `[]Provider`; impl in `broker/internal/llm`, fakes in tests.
  - [x] `var ErrAllProvidersFailed`.

- [x] **Task 2 — go-openai provider with GLM base-URL swap (`broker/internal/llm/openai.go`)** (AC: 3)
  - [x] `OpenAIProvider` wraps `*openai.Client`; `NewOpenAI(name, baseURL, model, httpClient)` sets `cfg.BaseURL` + `cfg.HTTPClient` and leaves `Token` empty (auth rides the transport).
  - [x] `Complete(ctx, []ChatMessage, model)` → `CreateChatCompletion` → first choice text; SDK error returned unchanged. **llm imports go-openai only** (no broker import — pure leaf).
  - [x] **Deviation from the literal plan:** the `Provider` interface + neutral types live in `broker` (so the worker can name them; `broker/internal/llm` is unimportable externally). `llm` keeps its own `ChatMessage` and a primitive `Complete` signature, and `broker` wraps it in an **adapter** (`openaiAdapter`) — this keeps `llm` a pure go-openai leaf and avoids a `broker ↔ llm` import cycle.

- [x] **Task 3 — Compose the chain with failsafe-go in the broker (`broker/broker.go`)** (AC: 1, 2, 3)
  - [x] `New()` resolves base URL (`SHELLDON_LLM_BASE_URL`, default GLM) + model (`SHELLDON_LLM_MODEL`) and wires the default GLM provider (via `openaiAdapter`) as the chain head, using the existing authtransport `Client()` + slog cred-resolution.
  - [x] `Complete(ctx, Request)`: per provider, `failsafe.With(retry, timeout).WithContext(ctx).Get(...)`; on success return; else log the fallback hop (slog, no prompt/key) and advance; on exhaustion return `ErrAllProvidersFailed` wrapping the last error.
  - [x] Kept `Client()`; updated the package doc.

- [x] **Task 4 — Tests (stdlib + httptest; no testify)** (AC: 1, 2, 3)
  - [x] **AC1 (fallback):** `broker/broker_test.go` `TestComplete_FallsBackToNextProvider` — fake #1 errors, #2 serves; asserts #2's reply and that both were attempted. Injected via `broker.NewWithProviders` (test-only seam in `export_test.go`).
  - [x] **AC2 (exhaustion):** `TestComplete_AllProvidersFail` — both fakes fail; `errors.Is(err, ErrAllProvidersFailed)`; both attempted. Deterministic (no network).
  - [x] **AC3 (base-URL swap):** `broker/internal/llm/openai_test.go` — httptest OpenAI-compatible server; asserts the reply parses AND the request hit `/chat/completions` on the swapped base URL. No real endpoint, no credits.
  - [x] **Fence live:** `broker/imports_test.go` passes (go-openai only under `broker/internal/llm`); `golangci-lint run` → 0 issues (the `provider-sdks-broker-internal-only` rule now polices a real, correctly-placed SDK).
  - [x] `go test -race ./...` → 69 pass; native + arm64 `CGO_ENABLED=0` builds succeed; `golangci-lint run` → 0 issues.

## Dev Notes

### Architecture constraints (binding)

- **AD-9 — Broker owns the ordered provider chain with retry/fallback.** "It owns the ordered **provider chain with retry/fallback** (default GLM via base-URL swap; alternates Ollama-LAN/OpenAI/OpenRouter/Anthropic), composed with **`failsafe-go`** (retry + breaker + timeout + fallback). Idiom: broker exposes only a pre-authorized `*http.Client` (an `http.RoundTripper` that injects auth); downstream code never sees the raw key. A `depguard` rule enforces that only `broker/internal/` may import provider/LLM SDKs." The go-openai client uses the 3.1 authtransport client as its `HTTPClient`, so the key rides the transport — go-openai never holds it. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-8 — A failed call never freezes the pet; chain exhaustion → reflex fallback.** "on provider-chain exhaustion the arbiter **falls back to a reflex behavior** so the pet never freezes." 3.2 guarantees the broker *returns an error* on exhaustion; the arbiter's reflex degradation already exists (Story 2.6 `ErrTurnTimeout`/worker-error → reflex ack) and connects via the worker in 3.3. [Source: ARCHITECTURE-SPINE.md#AD-8, 2-6 story]
- **AD-1 / depguard — provider SDKs only under `broker/internal/`.** go-openai is the first SDK; it lives in `broker/internal/llm/`. The `Provider` interface uses broker-local types so `broker/` composes the chain SDK-free. Enforced by `.golangci.yml` (`provider-sdks-broker-internal-only`) and `broker/imports_test.go`. [Source: ARCHITECTURE-SPINE.md#AD-1, .golangci.yml, broker/imports_test.go]
- **NFR8 — no creds on the bus; no creds to the SDK.** The credential is injected by the authtransport RoundTripper (3.1); go-openai's config `Token` stays empty — the key is never handed to the SDK or placed on any `Job`/`Result`. [Source: ARCHITECTURE-SPINE.md#AD-9, broker/internal/authtransport]
- **AD-17 — slog observability.** Log provider failures and fallback hops (which provider, what class of error) — never the prompt content, never the key. This is the provider-fallback logging the Epic 2 retro folded into 3.1/3.2. [Source: ARCHITECTURE-SPINE.md#AD-17, epic-2-retro-2026-06-22.md]
- **NFR2 / NFR3 — pure-Go, `CGO_ENABLED=0`, arm64.** go-openai and failsafe-go are pure-Go; verify the arm64 cross-build after `go get`. [Source: ARCHITECTURE-SPINE.md#Stack]
- **Deferred (config, not spine): exact model id + per-provider config** — GLM default via base-URL swap is broker config; the precise GLM base URL / model id are env-tunable, not invariants. [Source: ARCHITECTURE-SPINE.md#Deferred]

### Key design decisions

- **`Provider` interface with broker-local types is what keeps the fence real.** If the chain spoke go-openai types, `broker/` would import the SDK and break the fence. The interface returns plain `Response`/`Request` structs; only `broker/internal/llm/` touches `openai.*`. This is the whole reason 3.1 built `broker/internal/`.
- **Key rides the transport, not the SDK.** go-openai is configured with `cfg.HTTPClient = b.Client()` (the authtransport client) and an empty `Token`. Auth is injected by the RoundTripper for every provider uniformly — one credential mechanism, no per-SDK auth handling, and the key never enters go-openai's struct.
- **failsafe-go for resilience policies; ordered fallback across providers.** Use failsafe-go's retry + timeout per attempt; fallback advances down the ordered chain. If the cleanest expression is failsafe-go's Fallback policy chained per provider, use it; if an explicit ordered loop wrapping per-provider failsafe-executed calls reads clearer, that's acceptable — the AC is behavioral (inject 500 on #1 → #2 serves; all fail → `ErrAllProvidersFailed`). **Verify the current failsafe-go builder API before coding** — it changes between versions.
- **Test with fakes, not the network.** AC1/AC2 use in-memory fault-injecting `Provider` fakes (deterministic, no credits). AC3 uses an httptest OpenAI-compatible server to prove the go-openai base-URL swap + request/response translation. No test reaches a real provider.
- **`Complete` is the worker's egress; `Client()` stays for tool-calls.** 3.3's worker calls `broker.Complete`. `Client()` remains the AD-9 pre-authorized client (tool egress and any direct HTTP later); the providers are constructed from it.

### Previous story intelligence (Epic 1–3.1)

- **3.1 shipped the boundary this builds inside.** `broker.New()` resolves the key and builds `b.Client()` (the authtransport `*http.Client`); `broker/internal/authtransport` injects `Authorization: Bearer <key>` (and omits it when the key is empty — the degraded path). Reuse `b.Client()` as every provider's `HTTPClient`. [Source: broker/broker.go, broker/internal/authtransport/authtransport.go]
- **The depguard fence + import-walk test are already in place** and were built to activate now. `broker/imports_test.go` walks the repo, skips `broker/internal/`, fails on go-openai/anthropic/ollama imports elsewhere, with a `≥10` scanned-count guard. Putting the SDK under `broker/internal/llm/` is what makes both the lint rule and this test pass with a real import present. [Source: broker/imports_test.go, .golangci.yml]
- **Missing-key degradation already exists** — `New()` logs absence and builds an empty-bearer client without panicking; a chain built on it will fail at request time and surface `ErrAllProvidersFailed`, which is the AD-8 path. Don't re-add key-presence gating in the chain. [Source: broker/broker.go]
- **Error-as-Result, never panic across the bus** — the broker returns errors; the worker (3.3) maps a broker error to a degraded `Result`, and the arbiter (2.6) turns that into a reflex ack. Keep `Complete` returning plain errors. [Source: ARCHITECTURE-SPINE.md Consistency Conventions, 2-6 story]
- **slog idiom** — match the supervisor/scheduler/arbiter usage (`slog.Warn`/`slog.Error` with structured attrs). Log the provider name + error class on fallback; never the prompt or key. [Source: broker/broker.go, core/scheduler/scheduler.go]
- **Test-double pattern for fault injection** mirrors 2.6's `errWorker`/`hangingWorker` and the arbiter's `blockingWorker` — small in-test structs implementing the interface. [Source: core/dispatch/dispatch_test.go, core/arbiter/arbiter_test.go]
- **golangci-lint binary is at `~/go/bin/golangci-lint`** (not on PATH via the proxy); `go test -race ./...`, `CGO_ENABLED=0` native + `GOOS=linux GOARCH=arm64` are the gates. [Source: Epic 2–3.1 dev runs]

### Project Structure Notes

- New: `broker/provider.go` (interface + neutral types + `ErrAllProvidersFailed`), `broker/internal/llm/openai.go` (go-openai provider), `broker/internal/llm/openai_test.go` (AC3), extend `broker/broker_test.go` (AC1/AC2).
- Modified: `broker/broker.go` (chain build in `New()`, `Complete` method, package doc), `go.mod` + `go.sum` (add go-openai + failsafe-go).
- No `core/`, `contracts/`, `worker/`, reflexes, scheduler, or `main.go` change. `broker/imports_test.go` and `.golangci.yml` unchanged (they already encode the fence; they now have a real inhabitant to allow/deny).

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 3.2] — the three ACs (fallback on fault; exhaustion→reflex; GLM default base-URL swap)
- [Source: ...ARCHITECTURE-SPINE.md#AD-9] — broker owns the ordered provider chain (failsafe-go); GLM default via base-URL swap; auth-injecting client; SDKs only under broker/internal/
- [Source: ...ARCHITECTURE-SPINE.md#AD-8] — chain exhaustion → reflex fallback; a failed call never freezes the pet
- [Source: ...ARCHITECTURE-SPINE.md#AD-17, #Stack, #Deferred] — slog provider-fallback logging; failsafe-go + go-openai; GLM model/config is env-tunable
- [Source: broker/broker.go, broker/internal/authtransport/authtransport.go] — the 3.1 boundary to build inside (authtransport client, credential resolution)
- [Source: broker/imports_test.go, .golangci.yml] — the fence that now activates with the first real SDK
- [Source: core/arbiter/arbiter.go, 2-6 story] — the arbiter's existing degrade-to-reflex path that AC2's reflex behavior refers to
- [Source: _bmad-output/implementation-artifacts/epic-2-retro-2026-06-22.md] — AD-17 slog folded into broker stories (action item)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8 (1M context)

### Debug Log References

None. `go mod tidy` initially stripped the new deps (nothing imported them yet) — expected; they resolved after the code landed.

### Completion Notes List

- **The model egress is live behind the boundary.** `broker.Complete(ctx, Request)` runs an ordered provider chain; each provider gets a failsafe-go retry (2× / 100ms) + 30s timeout; failure advances to the next provider; total exhaustion returns `ErrAllProvidersFailed`. The default chain head is a GLM `OpenAIProvider` (base-URL swap on go-openai).
- **The depguard fence is now live and policing a real SDK.** go-openai is imported only in `broker/internal/llm`; `golangci-lint` (the `provider-sdks-broker-internal-only` rule) and `broker/imports_test.go` both pass with a real inhabitant present. The fence built vacuously in 3.1 now has teeth.
- **Layering deviation (flagged):** the story sketched the `Provider` interface + types in `broker` with the llm provider implementing it directly — that would force `llm → broker` (types) and `broker → llm` (construction) = import cycle. Resolved with an **adapter**: `llm` stays a pure go-openai leaf (its own `ChatMessage`, primitive `Complete`), and `broker.openaiAdapter` translates `broker.Request ↔ llm.ChatMessage`. `llm` imports zero broker code; only `broker → llm`. Behavior identical; the worker still names `broker.Request`/`broker.Response`.
- **Key still rides the transport, never the SDK.** go-openai is built with `cfg.Token = ""` and `cfg.HTTPClient = b.Client()` (the 3.1 authtransport client) — auth is injected by the RoundTripper for every provider uniformly.
- **AD-17 slog folded in** — provider failures + fallback hops are logged (provider name + error), never the prompt content or key.
- **AC2's "degrade to reflex" is the existing 2.6 path** — 3.2 only guarantees `Complete` returns a clean error on exhaustion; the worker (3.3) maps it to a degraded `Result` and the arbiter turns that into a reflex ack. No arbiter/worker change here.
- **Dependency footprint:** +2 direct (`go-openai`, `failsafe-go`), +1 indirect (`bits-and-blooms/bitset`). Both pure-Go; native + arm64 `CGO_ENABLED=0` build clean. (failsafe-go's grpc/protobuf transitive deps are not retained — we don't import its grpc subpackage.)
- **Not wired into `main.go`** — no caller until the real worker (3.3); `broker.New()` is constructed/tested in isolation.
- **Validation:** `go test -race ./...` → 69 pass (17 packages); `CGO_ENABLED=0` native + `GOOS=linux GOARCH=arm64` builds succeed; `golangci-lint run` → 0 issues.

### File List

- `broker/provider.go` (new) — `Provider` interface, `Message`/`Request`/`Response`, `ErrAllProvidersFailed`.
- `broker/internal/llm/openai.go` (new) — go-openai `OpenAIProvider` (base-URL swap, transport-injected auth) + `ErrTransient` error classification (review fix); the only go-openai importer.
- `broker/internal/llm/openai_test.go` (new) — AC3 base-URL-swap (atomic hitPath, review fix) + transient-classification test (review fix).
- `broker/broker.go` (modified) — chain build in `New()`, `Complete` (failsafe-go retry/timeout/fallback), `openaiAdapter`, package doc.
- `broker/broker_test.go` (modified) — AC1 fallback + AC2 exhaustion with fake providers.
- `broker/export_test.go` (new) — `NewWithProviders` test-only chain-injection seam.
- `go.mod` / `go.sum` (modified) — go-openai + failsafe-go (+ bitset indirect).

## Review Findings

- [x] [Review][Decision] **Failsafe timeout doesn't cancel in-flight HTTP requests** — resolved with option A: `Complete` now uses `GetWithExecution` and passes `exec.Context()` to the provider, so the failsafe Timeout policy's cancellation propagates into go-openai's `CreateChatCompletion(ctx)` and aborts the in-flight request (AD-8), not just future retries.
- [x] [Review][Patch] **Data race on `hitPath` in openai_test.go** — resolved: `hitPath` is now an `atomic.Value` (`Store`/`Load`). `go test -race` clean.
- [x] [Review][Patch] **"advancing chain" log fires for the last provider** — resolved: the "advancing chain" `slog.Warn` now fires only when a next provider exists (`i < len-1`); a `slog.Error("broker: all providers exhausted", …)` marks exhaustion before returning `ErrAllProvidersFailed`.
- [x] [Review][Patch] **Retry policy retries all errors including non-transient ones** — resolved: `llm` classifies errors (`ErrTransient` wraps 5xx `APIError` + transport `RequestError`); the broker retry policy adds `.HandleIf(errors.Is(err, llm.ErrTransient))`, so a 4xx falls through immediately. Classification lives in `llm` to keep go-openai behind the fence; covered by `TestComplete_ClassifiesTransientErrors`.
- [x] [Review][Defer] **`lastErr` is a `retrypolicy.ExceededError` wrapper, not the raw provider error** [`broker/broker.go:~112`] — deferred, pre-existing
- [x] [Review][Defer] **Max wall-clock per chain is unbounded without caller deadline** [`broker/broker.go:Complete`] — deferred, pre-existing
- [x] [Review][Defer] **`baseURL` trailing slash not sanitized** [`broker/broker.go:New()`] — deferred, pre-existing
- [x] [Review][Defer] **Empty `Messages` slice or empty `model` not validated at broker boundary** [`broker/broker.go:Complete`] — deferred, pre-existing

## Change Log

| Date       | Version | Description                                                                 |
| ---------- | ------- | --------------------------------------------------------------------------- |
| 2026-06-22 | 0.1     | Provider chain with retry/fallback (failsafe-go) behind the broker boundary; default GLM via go-openai base-URL swap under broker/internal/llm; depguard fence now live. All ACs satisfied; 69 tests pass. Status → review. |
