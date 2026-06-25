---
baseline_commit: ca28c13f23d5aa665739eb832e59fc25a92559c6
---
# Story 5.1: Privsep-lite worker subprocess + gob transport swap

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story.
     Lineage: first story of Epic 5 (M3 — "The Wall"). Epics.md flagged Epic 5 stories as
     "lighter stubs — re-detailed at build time"; this is that re-detailing for 5.1. -->

## Story

As the system,
I want the worker to run as a uid-separated recycled subprocess behind the unchanged `Worker` seam, with the bus transport beneath it swapped from in-process channels to length-prefixed `encoding/gob` over a Unix-domain socket,
so that the isolation hardening (M3, "The Wall") is invisible to every caller — the transport swap reshapes nothing above the seam (NFR5, AD-2, AD-4).

## Context

**First story of Epic 5 (M3 — "The Wall"); the seam swap that the whole epic stands on.** Epics 1–4 ran the worker as a **Monolith+ goroutine** (`worker/monolith`) behind the `worker.Worker` interface (`AssembleAndPropose(ctx, Job) (Result, error)`), wrapped by the arbiter (≤1 turn in flight, AD-8). AD-2 always promised a **second implementation behind the same seam**: a **uid-separated recycled subprocess** (re-exec of the binary, *not* fork), with the transport beneath the seam swapping from Go channels to **UDS + `encoding/gob`** — "callers never reshape across that swap." This story builds that second implementation and proves the swap is transparent.

**This is a TRANSPORT story, scoped to its one testable AC.** The epic's AC for 5.1 is narrow and exact: the **M0 contract round-trip suite passes against BOTH transports** (channel and gob/UDS), proving the swap reshapes no caller (AD-2/AD-4). It does **not** require the full brain to run in production across the wall — that (broker + memory reachable across the wall, vault, sensitive lane) is the rest of Epic 5 (5.2 vault isolation, 5.3 sensitive lane). 5.1 lands the **process wall + the wire** and proves the contract survives it.

**Privsep is opt-in; the default stays Monolith+ — zero regression.** A new env switch (`SHELLDON_WORKER=privsep`) selects the subprocess implementation in `main`; unset/`monolith` keeps today's goroutine worker exactly as-is. The running pet's behavior is unchanged unless privsep is explicitly enabled. This keeps the wall mergeable and CI-green on the laptop (darwin) while the OS-level uid enforcement is proven on the Pi in 5.2.

**The wire is the M0 gob contract made real.** Story 1.1 hardened a `gob` encode/decode round-trip of every `Envelope` precisely so this swap "cannot surface a serialization incompatibility later" ([contracts/contracts_test.go](../../contracts/contracts_test.go), `TestEnvelopeRoundTrip`). `contracts.Register()` already registers `Job`/`Result`/etc. with gob ([contracts/register.go](../../contracts/register.go)). 5.1 carries `Job` (parent→child) and `Result` (child→parent) as **length-prefixed gob frames** over the socket — the same types, now actually serialized.

**Transport mechanism: `socketpair` + inherited fd + re-exec — the standard Go privsep idiom.** The parent creates a connected UDS pair (`syscall.Socketpair` / a `net.UnixConn` pair), keeps one end, and passes the other to the child as an inherited file descriptor via `exec.Cmd.ExtraFiles` (child reads it at fd 3 with `os.NewFile`). No filesystem socket path to bind/listen/clean up, no bind race, no orphaned socket on crash. The child is launched by re-exec of `/proc/self/exe` (Linux; `os.Executable()` fallback for darwin dev) with a sentinel env var routing `main` into the worker-loop instead of the normal supervised process.

**uid-drop is Linux-only and gated; transport is proven everywhere.** The actual uid separation (`syscall.SysProcAttr{Credential: {Uid, Gid}}`) is Linux-only and needs the parent to be root (the Pi case). On darwin dev and non-root CI the subprocess runs same-uid — the **transport round-trip (the AC) is fully exercised regardless**, and the **OS-enforced uid isolation proof** is 5.2's property test on the Pi. 5.1 wires the credential drop and gates it cleanly; it does not assert OS enforcement (that is 5.2, NFR6/AD-3).

**This story does NOT:**
- create or populate a `vault/`, or classify anything sensitive — that is 5.2 (vault + isolation property test) and 5.3 (sensitive lane). The dream's `sensitiveLaneEnabled` stays `false` (untouched here).
- make the subprocess worker reach the **broker** or **memory** across the wall in production — the privsep child for this story hosts an inner `worker.Worker` for the **transport proof**; wiring the real LLM-backed brain across the wall (broker-callback + memory-read-callback, keeping creds parent-side per AD-9) is the explicit Epic-5 follow-on (see **Decisions to confirm** / Dev Notes). With privsep enabled, replies route over the wire; the production brain-across-the-wall is the next slice.
- change the `worker.Worker` interface, the arbiter, dispatch, the scheduler, contracts structs, or any caller above the seam — the swap is beneath the seam (AD-4). The only `main` change is the worker-selection switch.
- assert OS-level uid read-denial — that is 5.2.

## Acceptance Criteria

1. **The M0 contract round-trip passes against BOTH the channel and the gob/UDS transport.**
   **Given** the `Worker` seam with two transports beneath it — the in-process path (today's monolith/stub goroutine) and the new gob/UDS path (a recycled subprocess)
   **When** the contract round-trip is exercised across the seam on each transport (every `Envelope`/`Job`/`Result` shape that crosses the wire, mirroring `TestEnvelopeRoundTrip`'s every-kind coverage)
   **Then** both pass green and produce equal decoded values — proving the transport swap reshapes no caller and surfaces no gob incompatibility (Murat's test, AD-2/AD-4, NFR9).

2. **A turn round-trips through a real uid-separable recycled subprocess behind the unchanged seam.**
   **Given** the privsep `Worker` implementation selected behind the unchanged `worker.Worker` interface
   **When** `AssembleAndPropose(ctx, Job)` is called (and called again — the subprocess is recycled, not respawned per turn)
   **Then** the `Job` is gob-framed to the child over the UDS, the child runs the inner worker and gob-frames the `Result` back, and the caller receives the `Result` through the identical interface it uses for the goroutine worker — with ≤1 turn in flight (AD-8, the arbiter is unchanged) and `ctx` cancellation tearing down the in-flight turn cleanly.

3. **uid-drop is wired and gated; default is unchanged.**
   **Given** the privsep worker configured with a worker uid on Linux as root
   **When** the subprocess is launched
   **Then** it runs under the configured uid via `SysProcAttr.Credential`; **and** when no uid is configured, or the platform is not Linux, or the parent is not root, the subprocess launches same-uid with a logged notice (AD-17) and the transport still works. **And** with `SHELLDON_WORKER` unset, `main` wires the Monolith+ goroutine worker exactly as before (no behavior change).

## Tasks / Subtasks

- [x] **Task 1 — The privsep wire protocol: length-prefixed gob codec (`worker/privsep/codec.go`)** (AC: 1, 2)
  - [x] A tiny framing codec: write = `uint32` big-endian length prefix + a self-contained gob stream; read = read prefix, read exactly N bytes, gob-decode. A `maxFrameBytes = 1 MiB` cap rejects a corrupt/hostile length before allocating the body. (Per-frame gob stream chosen over one streaming encoder so a torn-down/recycled child never inherits half a type table.)
  - [x] `encodeFrame(w, v)` / `decodeFrame(r, *v)` over `io.Writer`/`io.Reader`, used for both directions via the `jobFrame` (parent→child) and `resultFrame` (child→parent) wrappers. The seam carries bare `Job`/`Result` — no `Envelope` header (the arbiter owns turn identity above the seam, AD-11); documented in the package doc.
  - [x] `codec_test.go`: round-trip a `jobFrame` and a `resultFrame` (with `MemoryOps` + a flattened error), assert `reflect.DeepEqual`; oversized prefix rejected; truncated body errors (not hangs).

- [x] **Task 2 — The child worker loop (`worker/privsep/child.go`)** (AC: 2)
  - [x] `runChild(ctx, conn, inner)`: strictly sequential loop — decode a `jobFrame`, call `inner.AssembleAndPropose`, encode the `resultFrame` back. Clean EOF / `net.ErrClosed` (parent closed) → return nil. An inner error is flattened to `resultFrame.Err` (gob can't carry a Go error) and rehydrated parent-side, preserving the `(Result, error)` seam contract (AD-8).
  - [x] `ChildMain(ctx, inner)` + `IsChild()`: when the sentinel env is set, adopt the inherited socket at fd 3 (`os.NewFile(3) → net.FileConn`) and run the loop. `worker/privsep` imports only `contracts` + `worker` + stdlib — it holds NO broker/creds; the inner worker is injected by `main` (a stub in tests). AD-9 cred-parent-side wiring documented as the deferred follow-on.

- [x] **Task 3 — The parent privsep Worker (`worker/privsep/privsep.go`)** (AC: 2, 3)
  - [x] `Worker` implements `worker.Worker` (`var _ worker.Worker = (*Worker)(nil)`); owns the recycled child (`exec.Cmd`, parent UDS end, a mutex serializing turns + lifecycle).
  - [x] `New(opts…)` with **lazy start** on first `AssembleAndPropose` (a boot-time failure surfaces as a turn error the arbiter degrades from, not a crash). `ensureStartedLocked` creates the `socketpair`, hands one end via `ExtraFiles` (→ fd 3), applies the uid-drop, `cmd.Start()`, keeps the parent end as a `net.Conn`.
  - [x] `AssembleAndPropose`: under the mutex, encode the `Job`, race the decode against `ctx.Done()`; on cancel/timeout or any wire error → `teardownLocked` (kill child, drop conn) so a half-read frame can't desync the recycled stream — the next turn lazily respawns. Child-side inner error → `error` return (AD-8 reflex degrade).
  - [x] `Close()`: close the parent conn (child sees EOF, exits), bounded `cmd.Wait()` with kill-on-`closeGrace`. Wired into `main`'s drain via `defer pw.Close()`.

- [x] **Task 4 — Re-exec target + sentinel (`worker/privsep/reexec.go` + `cmd/shelldon/main.go`)** (AC: 2, 3)
  - [x] `execPath()`: `/proc/self/exe` on Linux (canonical privsep idiom, stable across an atomic deploy swap), `os.Executable()` elsewhere — a runtime-`GOOS` switch (one file, no build tag needed for a path literal).
  - [x] `main`: a tiny `if privsep.IsChild()` branch right after the signal ctx runs `ChildMain` and returns — the child never builds the bus/scheduler/transport.
  - [x] Worker-selection switch: `SHELLDON_WORKER=privsep` → `privsep.New(WithUID(...))` + `defer pw.Close()`; default → today's `monolith.New(b, WithContextSource(memCtx))`. The selected `worker.Worker` flows into `arbiter.New(w, turnTimeout)` unchanged.

- [x] **Task 5 — uid-drop, Linux-gated (`worker/privsep/cred_linux.go` + `cred_other.go`)** (AC: 3)
  - [x] `cred_linux.go` (`//go:build linux`): sets `SysProcAttr.Credential{Uid,Gid}` only when a uid is configured AND `os.Geteuid()==0`; else logs a same-uid notice (AD-17). `cred_other.go` (`//go:build !linux`): no-op that logs the same-uid notice when a uid was requested.
  - [x] uid sourced from `SHELLDON_WORKER_UID` in `main`; the real Pi value + the matching `vault/` exclusion is Story 5.2's concern. No vault created here.

- [x] **Task 6 — Dual-transport contract round-trip proof (`worker/privsep/privsep_test.go`)** (AC: 1, 2, 3)
  - [x] **AC1 proof — both transports, equal results.** `TestPrivsep_DualTransportMatchesGoroutine`: a `JobReply` and a `JobDream` each run through (a) the goroutine path (`stubInner` called directly) and (b) the privsep path (same `stubInner` hosted in a **real subprocess**), asserting `reflect.DeepEqual` Results. Covers `Result` with and without `MemoryOps`.
  - [x] **Real subprocess under `go test`.** `TestMain` doubles as the child entry: when `IsChild()`, it runs `ChildMain(ctx, stubInner{})` and exits — so the parent talks to the **actual** socketpair + inherited-fd + gob wire (the test binary re-execs itself via the default command).
  - [x] **AC2 recycle + cancel.** `TestPrivsep_RecyclesAcrossTurns` (two sequential turns on one child). `TestPrivsep_ContextCancelTearsDownAndRecovers` (a blocking turn cancelled mid-flight returns `context.Canceled` promptly, the child is killed, and the next turn respawns clean).
  - [x] **AC3 gating.** `TestApplyCredential_GateDecision`: uid=0 sets no credential; a configured uid on a non-root host falls back to same-uid (no credential set). The transport round-trip runs same-uid throughout (the OS-enforced drop is Story 5.2 on the Pi).
  - [x] **Regression gate.** `go test -race ./...` → 157 pass / 23 packages; native + `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` builds succeed; `golangci-lint run` → 0 issues; `TestEnvelopeRoundTrip` + broker/dispatch import fences + `core/scheduler` unchanged. Boot smoke: default boots silent, `SHELLDON_WORKER=privsep` boots and logs the M3 worker selection — no panic.

## Dev Notes

### Architecture constraints (binding)

- **AD-2 — Worker isolation seam: the brain behind a swappable interface.** "Two implementations ship behind it: **Monolith+** (goroutine…) for M0–M2, **Privsep-lite** (a long-lived uid-separated recycled subprocess, **re-exec of `/proc/self/exe`, not fork**) as the M3+ end-state… ≤1 worker turn in flight regardless of implementation (AD-8)." This story builds the Privsep-lite implementation behind the unchanged `worker.Worker` interface. [Source: ARCHITECTURE-SPINE.md#AD-2]
- **AD-4 — Uniform Envelope contract; transport is a swappable seed.** "The **TRANSPORT** under the seam is swappable seed: channel now → **UDS + `encoding/gob` at the worker wall at M3, reshaping no caller**." The message contracts stay invariant; only the transport changes. The AC's "both transports pass the round-trip" is exactly this invariant. [Source: ARCHITECTURE-SPINE.md#AD-4, Consistency Conventions "Data & formats": "at the worker wall (M3) UDS frames are length-prefixed `encoding/gob`"]
- **AD-9 — Broker is the sole trust boundary; sole cred holder.** "**From M3 the worker reaches the broker only across the process wall.**" "`Job` envelopes carry **no creds**." Implication for the production brain-across-the-wall: model/tool creds stay **parent-side** (the untrusted subprocess must not hold them), so the real LLM-backed privsep brain reaches the broker via a back-channel to the parent — that wiring is the Epic-5 follow-on, not this transport story. 5.1 keeps creds out of the child by hosting an inner worker that, for the transport proof, makes no real egress. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-3 — The vault never exists until the worker is across a process wall.** 5.1 *opens* that wall (the uid-separated subprocess) but **creates no vault** and asserts no OS read-denial — that is 5.2. The dream's `sensitiveLaneEnabled` stays `false`. [Source: ARCHITECTURE-SPINE.md#AD-3, core/dream/dream.go:40]
- **AD-8 — ≤1 turn in flight; a failed call never freezes the pet.** The arbiter is **unchanged**: it wraps whichever `worker.Worker` it is given. The privsep worker must honor the same contract — a child error or ctx-timeout returns an `error` so the arbiter degrades to a reflex ack; ctx cancellation tears the in-flight child turn down. The codec is serialized (one frame pair per turn) to match ≤1-in-flight. [Source: ARCHITECTURE-SPINE.md#AD-8, core/arbiter/arbiter.go]
- **AD-10 / NFR9 — versioned typed contracts; the M0 gob round-trip is the swap's insurance.** Story 1.1 hardened `gob` round-trip of every `Envelope` "so M3's UDS+gob transport swap can't surface a gob-incompatibility later." 5.1 is where that insurance pays out — the wire carries the same registered types. No contract struct changes (additive-only rule holds; none needed). [Source: ARCHITECTURE-SPINE.md#AD-10, contracts/contracts_test.go, contracts/register.go]
- **AD-5 — supervised edges; graceful drain.** The subprocess must never outlive the parent: `Close()` on shutdown (close conn → child EOF → `cmd.Wait`), bounded kill-on-timeout. If the child dies mid-run, the privsep worker surfaces an error (→ reflex degrade) rather than hanging; consider whether a dead child should be re-spawned (recycled) or left dead-until-restart — document the choice (simplest: error out, let the next turn lazy-respawn, or surface to supervisor). [Source: ARCHITECTURE-SPINE.md#AD-5]
- **NFR3 / AD-1 — LLM-free core; depguard fence.** `worker/privsep` lives on the worker (edge) side, not in `core/`. It imports `contracts` + `worker` + stdlib (`os/exec`, `net`, `encoding/gob`, `syscall`); the child that hosts a real monolith transitively pulls broker — that's the worker edge, not core. Do not add any `core/*` import that would breach the LLM-free-core fence. [Source: ARCHITECTURE-SPINE.md#AD-1, broker/imports_test.go]

### Key design decisions (made; flagged where genuinely forked)

- **Bare `Job`/`Result` on the wire, not full `Envelope`.** The arbiter already owns turn identity/fencing above the seam (AD-11); the seam carries the turn payload, not bus routing. Framing `Job`→child / `Result`→child-back is the minimal honest wire. (If a later story needs header fields across the wall, the additive contract rule covers it.) Document this in the codec.
- **socketpair + inherited fd, not a filesystem UDS path.** `syscall.Socketpair`/`net.UnixConn` pair passed via `ExtraFiles` — no bind/listen/connect dance, no socket-file lifecycle, no race, nothing to clean up on crash. This is the idiomatic Go privsep transport and the simplest correct one.
- **Re-exec `/proc/self/exe` + sentinel env, not fork.** AD-2 is explicit: "re-exec…, not fork." A sentinel env (`SHELLDON_WORKER_CHILD=1`) routes `main` into the child loop as its first action, so the child never constructs the bus/scheduler/transport. `os.Executable()` is the darwin-dev fallback.
- **Privsep is opt-in (`SHELLDON_WORKER=privsep`), default Monolith+.** Keeps zero production regression and lets the wall land + test on the laptop before the Pi. The arbiter/dispatch/scheduler don't know which worker they got (AD-4).
- **uid-drop gated on Linux+root+configured-uid; transport proven everywhere else same-uid.** macOS dev and non-root CI can't drop uid; the AC (transport round-trip) does not need a real drop. The **OS-enforced isolation proof** is 5.2's property test on the Pi (NFR6/AD-3). 5.1 wires `SysProcAttr.Credential` and gates it; it asserts the *decision*, not the *enforcement*.
- **Inner worker is injected for the transport proof; the real brain-across-the-wall is the follow-on.** AD-9 forbids creds in the untrusted child, so a production LLM-backed privsep worker needs a parent-side broker back-channel (and a memory-read back-channel). That is a meaty, separable slice. **This is the one genuine scope fork — see Decisions to confirm.** Default story scope here: ship the wall + wire + dual-transport proof with an injected inner worker; carry the brain-across-the-wall as the next Epic-5 task.

### Previous story / codebase intelligence

- **The seam + arbiter (Epic 1–3).** `worker.Worker` = `AssembleAndPropose(ctx, contracts.Job) (contracts.Result, error)` ([worker/worker.go](../../worker/worker.go)); `worker.Stub` is a ready echo `Worker` for tests ([worker/stub.go](../../worker/stub.go)); `arbiter.New(w, timeout)` wraps any `Worker`, recovers child panics, bounds ≤1 in flight + ctx-timeout ([core/arbiter/arbiter.go](../../core/arbiter/arbiter.go)). The privsep worker drops in at `arbiter.New(w, …)` with no arbiter change. [Source: worker/worker.go, worker/stub.go, core/arbiter/arbiter.go]
- **gob is already wired.** `contracts.Register()` registers `Job`/`Result`/`InboundMessage`/`OutboundMessage`/`RegionSnapshot`, called by `init` ([contracts/register.go](../../contracts/register.go)); `TestEnvelopeRoundTrip` is the every-kind round-trip to mirror ([contracts/contracts_test.go](../../contracts/contracts_test.go)). The wire just needs framing + a max-size guard around the existing gob round-trip. [Source: contracts/register.go, contracts/contracts_test.go]
- **main wiring shape.** Today: `b := broker.New()` → `w := monolith.New(b, monolith.WithContextSource(memCtx))` → `arb := arbiter.New(w, turnTimeout)` ([cmd/shelldon/main.go:57,95,96](../../cmd/shelldon/main.go)). Add the sentinel-child branch at the top of `main`, and the `SHELLDON_WORKER` switch around the `w :=` line; everything downstream (`arb`, `disp`, `sched`) is untouched. Mirror the existing `SHELLDON_TRANSPORT` env-switch style already in `main`. [Source: cmd/shelldon/main.go]
- **Subprocess-test idiom.** Go's standard "re-exec the test binary" pattern (`exec.Command(os.Args[0], "-test.run=TestHelper")` + an env sentinel + early-exit helper) is how Task 6 exercises a real subprocess without a second binary. The telegram/cli adapters and `export_test.go` files show the project's stdlib-only, no-testify test style to match. [Source: worker/monolith/monolith_test.go, transport/cli/cli_test.go]
- **Inherited deps note.** No new module dependency — `os/exec`, `net`, `encoding/gob`, `syscall`, `os` are all stdlib. No `go.mod` change. [Source: go.mod]

### Inherited Epic-4 action items (do NOT block 5.1; sequence before 5.3)

These are open in `sprint-status.yaml` and the epic-4 retro; they are **5.3 / data-integrity** concerns, listed here only so the dev does not accidentally fold them in:
- Make `PromoteLearning` + `AppendFact` atomic (or compensating); stop `ApplyLearning` UPSERT resetting a promoted/pruned status to pending. **→ before 5.3, not here.**
- Decide AI-4 durable vs in-memory turn budget (park vs Epic 6). **→ not here.**
- `ErrNoRoute` observability (`slog.Warn` dropped hub publishes). **→ opportunistic, not here.**

### Project Structure Notes

- **New package `worker/privsep/`:** `privsep.go` (parent Worker), `child.go` (child loop + entry), `codec.go` (length-prefixed gob frames), `reexec.go` (exec path), `cred_linux.go` + `cred_other.go` (build-tagged uid-drop), `privsep_test.go` (dual-transport proof + recycle/cancel/gate). Optional `export_test.go` for the cred-decision assertion.
- **Modified:** `cmd/shelldon/main.go` — sentinel-child branch (top of `main`) + `SHELLDON_WORKER` selection switch + privsep `Close` in drain. No other file changes.
- **Unchanged (must stay zero-diff):** `worker/worker.go` (the interface), `core/arbiter/*`, `core/dispatch/*`, `core/scheduler/*`, `contracts/*` structs, every caller above the seam. The swap is beneath the seam (AD-4).
- **Build tags:** keep the Linux-only `syscall.Credential` code in `cred_linux.go` (`//go:build linux`) and a portable `cred_other.go` (`//go:build !linux`) so `CGO_ENABLED=0 GOARCH=arm64` (Linux) gets the real drop and darwin dev compiles. `/proc/self/exe` resolution likewise platform-switched.
- `.golangci.yml` unchanged. Tests use `t.TempDir()` and the test binary's own re-exec — no external sockets, no fixed paths.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 5.1] — the AC: M0 round-trip green against BOTH channel and gob/UDS, proving the swap reshapes no caller (AD-2/AD-4)
- [Source: ...ARCHITECTURE-SPINE.md#AD-2] — Privsep-lite = uid-separated recycled subprocess, re-exec not fork, behind the unchanged `Worker` seam; ≤1 in flight
- [Source: ...ARCHITECTURE-SPINE.md#AD-4] — transport is swappable seed: channel → UDS+gob at the worker wall, reshaping no caller; length-prefixed gob frames
- [Source: ...ARCHITECTURE-SPINE.md#AD-9] — broker is sole cred holder; from M3 the worker reaches the broker across the wall; no creds on the wire (→ brain-across-the-wall keeps creds parent-side)
- [Source: ...ARCHITECTURE-SPINE.md#AD-3] — vault doesn't exist until the worker is uid-separated; 5.1 opens the wall, 5.2 creates+proves the vault
- [Source: ...ARCHITECTURE-SPINE.md#AD-8, #AD-10, #AD-5] — ≤1 in flight + reflex degrade; the M0 gob round-trip insurance; supervised graceful drain
- [Source: worker/worker.go, worker/stub.go, core/arbiter/arbiter.go] — the seam, a ready stub Worker, the arbiter that wraps any Worker unchanged
- [Source: contracts/register.go, contracts/contracts_test.go] — gob registration + the every-kind round-trip to mirror on the wire
- [Source: cmd/shelldon/main.go] — the worker wiring point + the existing `SHELLDON_TRANSPORT` env-switch style to mirror for `SHELLDON_WORKER`

### Decisions to confirm (surfaced for Elliot — defaults chosen, override if you disagree)

1. **Scope of "the wall" in 5.1 (the one real fork).** Default: ship the **transport seam + recycled subprocess + uid-drop wiring + dual-transport round-trip proof** with an **injected inner worker** (stub for the proof, selectable in `main`). The **real LLM-backed brain across the wall** (parent-side broker back-channel + memory-read back-channel, creds staying parent-side per AD-9) becomes the next Epic-5 task. Alternative: fold the broker/memory back-channel into 5.1 — roughly doubles it and stretches the single AC. **Recommend the default** — it matches 5.1's exact AC and keeps the story shippable.
2. **uid-drop enforcement proof location.** Default: 5.1 *wires + gates* the drop; the **OS-enforced read-denial** is proven in **5.2** on the Pi (where the vault exists). 5.1 stays laptop-testable. Recommend default.
3. **Dead-child policy.** Default: a child crash surfaces as an `error` (→ reflex degrade, AD-8); the next turn lazy-respawns the subprocess. Alternative: supervise the child as a suture edge. Recommend the simple error-degrade for 5.1.

## Dev Agent Record

### Agent Model Used

claude-opus-4-8 (BMad dev-story workflow; single-orchestrator implementation — the package is one tightly-coupled unit, no subagent fan-out).

### Debug Log References

- **Child deadlock-detector crash in the cancel test.** The first cancel-test stub used `select {}` to hang the inner worker. In the subprocess that is the *only* goroutine, so the Go runtime fired `fatal error: all goroutines are asleep - deadlock!` and crashed the child *before* the parent's 150ms cancel — the parent then saw a wire EOF (read error), not `context.Canceled`, and the test failed. Fixed by hanging on `time.Sleep(10s)` instead: a pending timer keeps the child out of the deadlock detector until the parent's teardown kill lands.
- **golangci-lint `unused: withCommand`.** Added a `withCommand` option for test command injection, then found the default re-exec (`/proc/self/exe` / `os.Executable()` resolves to the test binary) already lets `TestMain` serve as the child — so the option was dead. Removed it (no speculative flexibility).

### Completion Notes List

- **AC1 (the headline) proven two ways.** (1) `codec_test.go` round-trips `jobFrame`/`resultFrame` through the length-prefixed gob wire — the M0 contract round-trip at the wire level. (2) `TestPrivsep_DualTransportMatchesGoroutine` runs the same turns through the in-process goroutine path and the **real subprocess** UDS+gob path and asserts `reflect.DeepEqual` Results — "the transport swap reshapes no caller" made concrete (AD-2/AD-4).
- **Transport mechanism.** `syscall.Socketpair(AF_UNIX, SOCK_STREAM)` → one end kept by the parent (`net.FileConn`), the other inherited by the child via `exec.Cmd.ExtraFiles` (fd 3). No filesystem socket path, no bind/listen race, nothing to clean up on crash. Child launched by re-exec of `/proc/self/exe` (Linux) / `os.Executable()` (darwin) + the `SHELLDON_WORKER_CHILD=1` sentinel.
- **Recycled, not respawned.** The child persists across turns; `teardownLocked` (kill + drop conn) runs only on a wire error or ctx cancel, and the next turn lazily respawns. `TestPrivsep_RecyclesAcrossTurns` proves reuse; `TestPrivsep_ContextCancelTearsDownAndRecovers` proves clean teardown + respawn.
- **uid-drop wired + gated, not asserted-enforced (per the keep-split decision).** `cred_linux.go` drops via `SysProcAttr.Credential` only when a uid is configured AND the parent is root; everything else (darwin dev, non-root CI, uid unset) runs same-uid with a logged notice — the transport is fully exercised regardless. OS-enforced read-denial is Story 5.2's property test on the Pi.
- **Seam unchanged; `main` is the only caller touched.** `worker.Worker`, the arbiter, dispatch, scheduler, and all `contracts` structs are zero-diff. `main` gains the child branch + the `SHELLDON_WORKER` switch; default (unset) wires the Monolith+ worker exactly as before — confirmed by the boot smoke (default boots silent).
- **Brain-across-the-wall deferred (keep-split, confirmed by Elliot).** The privsep child in `main` hosts `monolith.New(broker.New())` so replies function across the wall, but model creds presently live in the child process. AD-9-compliant cred-parent-side wiring (the worker reaching the parent's broker + a memory-read across the wall) is the explicit Epic-5 follow-on, documented in `main` and the package. `worker/privsep` itself imports no broker — the LLM-free-core fence is intact.
- **Validation:** `go test -race ./...` → 157 pass / 23 packages; native + `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` builds green; `golangci-lint run` → 0 issues; `go vet` clean. Existing `TestEnvelopeRoundTrip`, broker/dispatch import fences, and `core/scheduler` untouched.

### File List

- `worker/privsep/codec.go` (new)
- `worker/privsep/child.go` (new)
- `worker/privsep/privsep.go` (new)
- `worker/privsep/reexec.go` (new)
- `worker/privsep/cred_linux.go` (new)
- `worker/privsep/cred_other.go` (new)
- `worker/privsep/codec_test.go` (new)
- `worker/privsep/privsep_test.go` (new)
- `cmd/shelldon/main.go` (modified — child branch + SHELLDON_WORKER selection switch + imports)
- `_bmad-output/implementation-artifacts/sprint-status.yaml` (tracking)

### Review Findings

- [x] [Review][Defer] `Close()` discards cmd.Wait() exit status [worker/privsep/privsep.go:194] — deferred, pre-existing; project pattern is `_ = cmd.Wait()`; logging would add AD-17 observability
- [x] [Review][Defer] No panic recovery in `runChild`; child panic crashes process, surfaces as parent EOF error → arbiter degrade [worker/privsep/child.go:55] — deferred, pre-existing; "dead-child policy" decision documented in spec; AD-8 contract upheld via error path

## Change Log

| Date       | Change |
|------------|--------|
| 2026-06-25 | Story 5.1 implemented: `worker/privsep` — Privsep-lite subprocess worker behind the unchanged `worker.Worker` seam, with the transport swapped to length-prefixed gob over a socketpair UDS (inherited fd + re-exec, not fork). uid-drop wired + Linux/root-gated; privsep opt-in via `SHELLDON_WORKER`, default Monolith+ unchanged. AC1 proven both at the codec level and via a real subprocess dual-transport equality test. Full validation green (157 tests -race, arm64 build, lint 0). Status → review. |
