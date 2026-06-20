---
baseline_commit: c3e87171cb1b7cf9ed2da49f57deaffe04afbb56
---

# Story 1.1: Versioned contracts + gob round-trip

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As a developer building sheldon,
I want versioned `Envelope`/`Job`/`Result` Go structs with the closed header in `contracts/` that round-trip through `encoding/gob`,
so that the M3 UDS+gob transport swap cannot surface a serialization incompatibility later and the contract stays a binding invariant the whole system depends on.

## Context

This is the **first code in the repository** — there is no `go.mod` yet. The story therefore also bootstraps the Go module. It is the keystone of Epic 1 (the Tracer Bullet): every later component speaks `Envelope`/`Job`/`Result`, so freezing this contract shape correctly now is what makes the M3 isolation swap a pure transport change rather than a redesign (AD-2, AD-4). Keep scope tight — this story defines the contract types and proves they serialize; it does **not** build the hub (1.2), the worker seam (1.3), or any behavior.

## Acceptance Criteria

1. **Gob round-trip covers every kind.**
   **Given** the `Envelope`/`Job`/`Result` structs with the closed header (`id/v/kind/src/dst/turn_id`) defined in `contracts/`
   **When** every `Envelope` kind is gob-encoded and decoded in the required M0 round-trip test
   **Then** each decoded value is deep-equal to the original
   **And** the test covers every declared envelope/payload kind, not a representative sample (NFR9, AD-10).

2. **Additive evolution is non-breaking.**
   **Given** a contract value carrying a `v` version field
   **When** an additive field is appended to a payload struct
   **Then** a decoder built against the older struct shape still decodes the value without error, and a newer decoder reading older-encoded bytes leaves the new field at its zero value (additive-only evolution, NFR9/AD-10).

3. **Contracts package is clean and cross-compiles.**
   **Given** the contracts package compiled
   **When** `depguard` (via golangci-lint) runs over `contracts/`
   **Then** no provider/LLM SDK import is present (NFR3), and the package builds with `CGO_ENABLED=0 GOARCH=arm64` (NFR2).

## Tasks / Subtasks

- [x] **Task 0 — Bootstrap the Go module** (AC: 3)
  - [x] `go mod init github.com/elliotboney/sheldon_go` (module path confirmed with Elliot to match the actual git remote — underscore, not the hyphen guessed in Dev Notes)
  - [x] Set the Go version directive in `go.mod` to `1.25` (matches the spine Stack; `testing/synctest` GA, used by later stories)
  - [x] Add a `.golangci.yml` with a `depguard` rule (see Dev Notes for the exact rule); minimal for now — the full LLM-free-core fence is wired in Story 3.1
- [x] **Task 1 — Define the closed header and Envelope** (AC: 1)
  - [x] Create `contracts/envelope.go` with `Header{ ID, V, Kind, Src, Dst, TurnID }` — the closed header from AD-11, no extra fields
  - [x] Define `Kind` as a string type with a **closed set of typed constants** (start with the core kinds: `KindJob`, `KindResult`; event kinds are added by the stories that introduce them — do NOT invent speculative kinds)
  - [x] Define `Envelope{ Header; Payload }` where `Payload` is an interface (`isPayload()` marker) so concrete payloads are gob-encodable
- [x] **Task 2 — Define Job and Result payloads** (AC: 1)
  - [x] `contracts/job.go` — `Job` (what core dispatches to the worker; the *turn* input). Keep fields minimal and honest to what M0 needs; no creds field ever (NFR8)
  - [x] `contracts/result.go` — `Result` (what the worker *proposes* back: reply text + proposed memory-ops slice, kept as a typed placeholder for now). The worker never writes; this is the proposal channel (AD-6)
  - [x] Both implement the `Payload` marker
- [x] **Task 3 — gob registration** (AC: 1)
  - [x] Provide `contracts.Register()` (or a package `init()`) calling `gob.Register(Job{})` and `gob.Register(Result{})` — gob requires concrete types behind an interface to be registered before encode/decode
- [x] **Task 4 — Required M0 round-trip test** (AC: 1)
  - [x] `contracts/contracts_test.go` — table-driven test: one row per declared kind/payload; encode an `Envelope` to a `bytes.Buffer` via `gob.NewEncoder`, decode via `gob.NewDecoder`, assert `reflect.DeepEqual` (or per-field equality) to the original
  - [x] The table is derived so adding a new `Kind` without a test row **fails** (e.g. assert every constant in the closed set is exercised) — this enforces AC1's "every kind, not a sample"
- [x] **Task 5 — Additive-evolution test** (AC: 2)
  - [x] In the test file, define a `jobV1` shape (a subset of `Job`'s fields) and prove cross-decoding both directions succeeds (encode `Job` → decode into `jobV1`; encode `jobV1` → decode into `Job` with new field zero-valued)
- [x] **Task 6 — Verify build + lint** (AC: 3)
  - [x] `CGO_ENABLED=0 GOARCH=arm64 go build ./...` succeeds (cross-compile target; can run from the laptop)
  - [x] `go test ./contracts/...` passes
  - [x] `golangci-lint run` passes with the depguard rule active

### Review Findings

- [x] [Review][Decision→Defer] AllKinds mutability and Kind-AllKinds sync gap — deferred; unsure which fix approach to take (unexported+Kinds() vs exported+comment). Revisit when a second Kind is added.
- [x] [Review][Patch] TestAdditiveEvolution only covers Job — add `resultV1` struct (subset of `Result` fields) and two sub-tests proving both directions of evolution for `Result`/`MemoryOp` [contracts/contracts_test.go] — RESOLVED: added `resultV1` + `TestResultAdditiveEvolution` (new→old drops `MemoryOps`, old→new zero-fills it)
- [x] [Review][Defer] gob type names include module path — if module is forked/renamed, existing gob blobs produce "type not registered"; no test guards this [contracts/register.go] — deferred, pre-existing
- [x] [Review][Defer] Header.V is defined but nothing reads or gates on it — intentional per architecture; version negotiation is future work [contracts/envelope.go] — deferred, pre-existing
- [x] [Review][Defer] No negative test for gob type-not-registered path — future bus code should verify the error is catchable rather than a panic [contracts/register.go] — deferred, pre-existing
- [x] [Review][Defer] nil Payload in Envelope is untested — gob behavior with nil interface field is unvalidated; relevant when bus enforces non-nil before encoding [contracts/contracts_test.go] — deferred, pre-existing

## Dev Notes

### Architecture constraints (binding)

- **The closed header is exactly `id/v/kind/src/dst/turn_id`** — no more, no less. Adding header fields is a contract change, not a story liberty. [Source: ARCHITECTURE-SPINE.md#AD-11]
- **`Envelope`/`Job`/`Result` are the uniform contract over the bus**, passed as Go structs (no serialization in-process); gob serialization only happens at the worker wall at M3. Story 1.1 builds the gob path *now* precisely so M3 is a transport swap, not a redesign — that is the entire point of this story. [Source: ARCHITECTURE-SPINE.md#AD-4]
- **Go structs ARE the typed contract** (no msgspec equivalent needed). Versioning = the `v` header field + **additive-only** struct fields. Never remove or reorder fields; only append. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **No credentials ever on the bus** — no `Job`/`Result`/`Envelope` field may carry a model/tool credential. The broker injects creds internally at egress (Story 3.1). [Source: ARCHITECTURE-SPINE.md#AD-9, SPEC NFR8]
- **`turn_id` is the fencing field** (AD-12, idempotent close) — it lives in the header now even though fencing logic arrives in Story 1.3/1.4. Define it; don't implement fencing here. [Source: ARCHITECTURE-SPINE.md#AD-11, #AD-12]
- **`contracts/` has no LLM/provider imports** — trivially true here (it's pure structs), but the depguard rule establishes the pattern the whole repo relies on. [Source: ARCHITECTURE-SPINE.md#AD-1, SPEC NFR3]

### Required M0 test (this story owns one of the four)

The spine names four required M0 tests; **this story delivers the contract gob round-trip**. The other three (≤1-worker, atomic-write crash-safety, soul-survives-edge-panic) belong to Stories 1.3, 1.6, and 1.4. Do not stub those here. [Source: ARCHITECTURE-SPINE.md#AD-10, epics.md NFR9]

### gob specifics (prevent the common traps)

- gob encodes **exported fields only** — every contract field must be capitalized.
- An interface-typed field (`Payload`) requires `gob.Register` of each concrete type *before* encode/decode, or you get `gob: type not registered`. That is what Task 3 exists for.
- gob is **tolerant by design**: unknown fields on the wire are ignored; missing fields decode to zero. This is *why* additive evolution works (AC2) — lean on it, don't fight it.
- Compare decoded vs original with `reflect.DeepEqual` for the round-trip; for the additive test, compare only the overlapping fields.
- gob is stdlib and stable — no external dependency, no version pin needed.

### Project structure (greenfield — you are creating it)

```
sheldon_go/
  go.mod                  # NEW — Task 0
  .golangci.yml           # NEW — depguard rule
  contracts/              # NEW — this story's package
    envelope.go           # Header, Kind + constants, Envelope, Payload interface
    job.go                # Job
    result.go             # Result
    register.go           # contracts.Register() / init() with gob.Register
    contracts_test.go     # round-trip + additive-evolution tests
```
This matches the spine's Structural Seed (`contracts/` = versioned Go Envelope/Job/Result). Later siblings — `core/ broker/ worker/ transport/ display/ plugins/ tests/` — arrive with their stories; do **not** scaffold them now. [Source: ARCHITECTURE-SPINE.md#Structural Seed]

### Exact depguard rule (minimal, for `.golangci.yml`)

```yaml
linters:
  enable: [depguard]
linters-settings:
  depguard:
    rules:
      contracts-pure:
        files: ["**/contracts/**"]
        deny:
          - pkg: "github.com/anthropics/anthropic-sdk-go"
            desc: "contracts must not import provider/LLM SDKs (AD-1)"
          - pkg: "github.com/sashabaranov/go-openai"
            desc: "contracts must not import provider/LLM SDKs (AD-1)"
```
Pin the golangci-lint version in CI when CI lands (Story-level concern later) — the depguard config schema has shifted across golangci-lint versions, so a floating version can silently break the rule. [Source: research review-tech-currency.md]

### Testing standards

- **Table-driven tests + stdlib `testing`** are the project default. `testify/require` is acceptable as a thin assertion layer but is not required for this story (stdlib `reflect.DeepEqual` suffices). [Source: research/technical-language-selection...#Testing]
- No hardware, no network, no clock in this story — it's pure value serialization. (Later stories use `testing/synctest` for cadences; not here.)
- The round-trip test is one of the four **required M0 tests** — it must pass on the Pi as part of the Epic 1 exit criteria (Story 1.6 runs the suite on-device); for this story, passing on the laptop with the arm64 cross-build succeeding is sufficient.

### Git intelligence

Recent commits are all planning artifacts (`0227e40` initial planning stack → `1bbf8a3` README links) — **no Go code exists yet**. This story creates the first. There is no prior code pattern to match; you are establishing the conventions (package layout, test style) that Stories 1.2–1.6 will follow, so keep them boring and idiomatic.

### Project Structure Notes

- Greenfield: no variance to reconcile. The package layout above is the authoritative seed from the spine; follow it exactly so later stories slot in without churn.
- **Open question (module path):** `github.com/elliotboney/sheldon-go` is the assumed module path (the Go repo has no git remote set yet; the Python original is `github.com/elliotboney/shelldon`). If you intend a different remote/module path, set it in `go mod init` accordingly — changing it later means rewriting every import. Confirm before Task 0.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 1.1] — ACs, epic goal
- [Source: _bmad-output/planning-artifacts/architecture/architecture-sheldon_go-2026-06-19/ARCHITECTURE-SPINE.md#AD-4] — uniform Envelope, transport-as-seed
- [Source: ...ARCHITECTURE-SPINE.md#AD-10] — versioned contracts, required M0 tests, additive evolution
- [Source: ...ARCHITECTURE-SPINE.md#AD-11] — closed header id/v/kind/src/dst/turn_id, routing modes
- [Source: ...ARCHITECTURE-SPINE.md#AD-1] — LLM-free packages, depguard
- [Source: _bmad-output/specs/spec-sheldon-go/SPEC.md] — NFR2 (CGO_ENABLED=0/arm64), NFR8 (no creds on bus), NFR9 (required tests)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Claude Opus 4.8, 1M context) via Claude Code dev-story workflow.

### Debug Log References

- Module path open question resolved with Elliot: chose `github.com/elliotboney/sheldon_go` (underscore) to match the actual git remote `https://github.com/elliotboney/sheldon_go.git`, overriding the Dev Notes guess of `sheldon-go` (hyphen). Changing the path later would mean rewriting every import.
- `golangci-lint` was not installed; installed v2.12.2 via `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`. The `.golangci.yml` uses the golangci-lint **v2** config schema (`version: "2"`, linters under `settings:`), not the v1 schema shown in Dev Notes — the v1 form is silently ignored by v2. The Dev Notes already flagged this schema drift as a risk.
- Verified depguard is not a no-op: temporarily denied `encoding/gob` (a package the code imports) and confirmed depguard flagged it in `register.go` and `contracts_test.go`, then reverted to the provider-SDK deny list.

### Completion Notes List

- **AC1 satisfied** — `TestEnvelopeRoundTrip` is table-driven keyed by `Kind` and iterates `AllKinds`; a declared kind with no case fails the test (enforces "every kind, not a sample"). Both `KindJob` and `KindResult` round-trip deep-equal through `gob`.
- **AC2 satisfied** — `TestAdditiveEvolution` proves both directions: a newer `Job` (with appended `ConvoID`) decodes into the older `jobV1` shape without error, and older `jobV1` bytes decode into `Job` with `ConvoID` left at its zero value.
- **AC3 satisfied** — `CGO_ENABLED=0 GOARCH=arm64 go build ./...` succeeds; `golangci-lint run` reports 0 issues with the `depguard` `contracts-pure` rule active over `contracts/`. No provider/LLM SDK imports present (the package imports only stdlib `encoding/gob`).
- Scope held tight: contract types + gob proof only. No hub (1.2), worker seam (1.3), fencing logic, or speculative kinds. `turn_id` defined in the header but fencing not implemented (belongs to 1.3/1.4). `MemoryOp` is a typed placeholder (one `Kind` field) — real vocabulary arrives in Epic 4.
- No `go.sum` produced: the module has zero external dependencies (stdlib only).

### File List

- `go.mod` (new) — module `github.com/elliotboney/sheldon_go`, go 1.25
- `.golangci.yml` (new) — golangci-lint v2 config with the `contracts-pure` depguard rule
- `contracts/envelope.go` (new) — `Header`, `Kind` + constants, `AllKinds`, `Payload` interface, `Envelope`
- `contracts/job.go` (new) — `Job` payload
- `contracts/result.go` (new) — `Result` + `MemoryOp` placeholder
- `contracts/register.go` (new) — `Register()` + `init()` gob registration
- `contracts/contracts_test.go` (new) — round-trip + additive-evolution tests
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (modified) — story status → in-progress → review

## Change Log

| Date       | Change                                                                 |
|------------|------------------------------------------------------------------------|
| 2026-06-20 | Bootstrapped Go module and implemented versioned `Envelope`/`Job`/`Result` contracts with gob round-trip + additive-evolution tests (Story 1.1). All 6 tasks complete; build, tests, and lint green. |
| 2026-06-20 | Addressed code review findings — 1 [Patch] resolved (added `resultV1` + `TestResultAdditiveEvolution` for Result/MemoryOp additive evolution); 5 findings deferred. Tests 6→9, lint clean. |
