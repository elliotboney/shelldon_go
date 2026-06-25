---
baseline_commit: a1da2dd3293b6d208fe9922a04c8c5513a1c6975
---
# Story 5.2: Vault + isolation property test

Status: ready-for-dev

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story.
     Lineage: second story of Epic 5 (M3 — "The Wall"). 5.1 opened the process/uid wall
     (privsep subprocess + UDS+gob transport) and wired+gated the uid-drop but asserted NO
     OS enforcement. 5.2 is that enforcement proof: create the vault excluding the worker uid
     and prove — as a property test on the Pi — that a worker-uid process cannot read it. -->

## Story

As the system,
I want the `vault/` created with directory permissions that exclude the worker uid, and a property test that proves a process running as the worker uid cannot read vault contents,
so that vault isolation is **OS-enforced, not a path filter** — the read is denied by the kernel, closing the M3 confidentiality gate (NFR6, AD-3).

## Context

**Second story of Epic 5 (M3 — "The Wall"); the enforcement proof that 5.1's wall was built to carry.** Story 5.1 landed the *mechanism* of the wall: a uid-separable recycled subprocess behind the unchanged `worker.Worker` seam, with the transport swapped to length-prefixed `gob` over a socketpair UDS, and the uid-drop wired + gated (`worker/privsep/cred_linux.go` sets `SysProcAttr.Credential` only on Linux + root + a configured non-zero uid). 5.1 explicitly **asserted the gating *decision*, not the OS *enforcement*** — it left "OS-enforced uid read-denial" as this story's job (see 5.1 AC3 + Decisions-to-confirm #2). 5.2 collects that debt: it creates the `vault/` with permissions that exclude the worker uid, and proves with a property test that a process *as that uid* is denied the read.

**This is the AD-3 invariant made testable.** AD-3 ("the vault never exists until the worker is across a process wall") has two halves. Half one — *the vault doesn't exist before M3* — is already enforced and tested: `core/memory/curated.go` rejects any `vault/` write with `ErrOwnerOnly` and creates nothing (`curated_test.go:157-162`, `:279-283`), and the dream cycle's `sensitiveLaneEnabled` stays `false` so nothing is ever routed to a vault (`core/dream/dream.go:34-38`, `dream_test.go:199-200`). Half two — *at M3+, `vault/` permissions exclude the worker uid (OS-enforced, not a path-filter)* — is **this story**. The phrase "OS-enforced, not a path-filter" is the whole point: confidentiality must survive a worker that ignores or subverts the `curated.go` path rejection, because the worker is untrusted-by-design (it assembles prompts from web-influenced content). The kernel, not Go code, denies the read.

**Mechanism: a `0700` vault dir owned by the core (parent) uid — any other uid is excluded by the kernel.** The worker subprocess is dropped to a *different, unprivileged* uid (5.1, `SHELLDON_WORKER_UID`). A `vault/` directory created mode `0700` and owned by the core/parent uid is, by POSIX rules, unreadable and untraversable to every other uid — including the worker uid. No ACLs, no xattrs, no path filter: plain owner-exclusion is the simplest correct OS enforcement and is exactly what AD-3 ("`vault/` permissions exclude the worker uid") calls for. The property test proves the negative: a process as the worker uid gets `EACCES` (`fs.ErrPermission`) on both `os.ReadFile(vault/secret)` and traversal of `vault/`.

**The enforcement is real only on the Pi (Linux + root + worker-uid ≠ core-uid); everywhere else it skips with a logged reason.** This mirrors 5.1 exactly. On darwin dev and non-root CI the worker can't actually be dropped to a separate uid, so the worker process runs *as the core uid* and **can** read a `0700` core-owned vault — the property cannot hold and the test must `t.Skip` with a clear reason (not fail, not silently pass). The OS-enforced denial is asserted only where the OS can enforce it: Linux, parent is root, able to drop to a distinct unprivileged uid. A fast, platform-independent **structural** assertion (the vault is created mode `0700`, owned by the creating uid, and `curated.WriteFile`/`AppendFact` still reject it) runs everywhere to guard the wiring on the laptop.

**Vault creation is core-owned and gated on the worker being uid-separated (AD-3).** The vault is **not** created by the bot/LLM path — `curated.go` rejects `vault/` and must keep rejecting it (that disjoint-writer invariant is load-bearing). Core creates the empty `vault/` directly, with the exclusion perms, and only when the worker is actually uid-separated (`SHELLDON_WORKER=privsep` + a non-zero `SHELLDON_WORKER_UID` on Linux+root). Before M3 / without a configured worker uid there is still nothing for a goroutine-worker to read (AD-3 half one holds untouched).

**This story does NOT:**
- turn on the sensitive-classification lane (`sensitiveLaneEnabled` stays `false`) or route any learning into the vault — that is **5.3**. 5.2 creates an *empty, isolated* vault and proves the isolation; it populates nothing.
- wire the worker's memory-read back-channel across the wall, or broker-gated surfacing of vault contents into a prompt (AD-9) — those remain the Epic-5 follow-ons flagged in 5.1.
- change the `worker.Worker` interface, the arbiter, dispatch, the scheduler, the contracts, or the privsep transport (5.1) — 5.2 adds vault creation + a property test; it reshapes no caller.
- relax `curated.go`'s `vault/` rejection — that invariant stays exactly as-is; the property test is **additive**.

## Acceptance Criteria

1. **The vault is created with permissions that exclude the worker uid (OS-enforced, not a path-filter).**
   **Given** the worker is uid-separated (privsep + a configured worker uid distinct from the core uid)
   **When** core ensures the `vault/` directory under the curated memory root
   **Then** `vault/` exists as a directory owned by the core (parent) uid with mode `0700`, so the kernel excludes every other uid — including the worker uid — from reading or traversing it (NFR6/AD-3); **and** the existing `curated.WriteFile`/`AppendFact` `vault/` rejection (`ErrOwnerOnly`, nothing created) is unchanged.

2. **A property test proves a worker-uid process cannot read the vault.**
   **Given** a `vault/` directory created mode `0700` owned by the core uid, containing a secret file, on Linux as root
   **When** a child process dropped to a distinct unprivileged worker uid (via `SysProcAttr.Credential`, the 5.1 mechanism) attempts to read a file inside `vault/` and to traverse `vault/`
   **Then** both attempts are denied by the kernel — the child receives a permission error (`fs.ErrPermission` / `EACCES`), never the secret bytes — proving isolation is OS-enforced (the AD-3 property, NFR6).

3. **Off the enforcing platform the property test skips with a reason; the structural guard runs everywhere; default is unchanged.**
   **Given** a host that is not Linux, or where the parent is not root, or where a distinct worker uid cannot be dropped
   **When** the property test runs
   **Then** it `t.Skip`s with a logged reason (the OS cannot enforce the drop here — proof is Pi-only), **and** a platform-independent structural test still asserts the vault is created `0700` + core-owned and that `curated.go` still rejects `vault/`; **and** with `SHELLDON_WORKER` unset (or no worker uid configured) `main` creates **no** vault and behaves exactly as before (no regression).

## Tasks / Subtasks

- [ ] **Task 1 — Core vault creation helper (`core/memory/`)** (AC: 1, 3)
  - [ ] Add an exported core-owned helper (suggested `EnsureVault(curatedRoot string) (string, error)` on the `memory` package, or a method on `*Curated`) that creates `<curatedRoot>/vault/` with `os.MkdirAll(path, 0o700)` and, after creation, `os.Chmod(path, 0o700)` to defeat umask (so the mode is exactly `0700`, not `0700 &^ umask`). It is owned by the running (core) process uid by construction. Idempotent: a second call on an existing `0700` vault is a no-op success.
  - [ ] This is a **core-direct** create — it does NOT go through `curated.WriteFile`, and it does NOT relax that path's `vault/` rejection (`curated.go:63-66`). The disjoint-writer invariant (bot/LLM may never create or write the vault) is untouched; only core, here, creates the empty dir.
  - [ ] Package fences: `core/memory` stays LLM-free-core (AD-1) — stdlib `os`/`path/filepath` only; no broker/provider import.

- [ ] **Task 2 — Structural test, runs everywhere (`core/memory/*_test.go`)** (AC: 1, 3)
  - [ ] `TestEnsureVault_Perms`: under `t.TempDir()`, call the helper; assert the `vault/` dir exists, `FileInfo.Mode().Perm() == 0o700`, and (where resolvable) its owner uid equals `os.Getuid()`. Assert idempotency (second call succeeds, mode still `0700`).
  - [ ] `TestEnsureVault_DisjointWriterUnchanged` (or extend existing `curated_test.go`): after `EnsureVault`, `curated.WriteFile("vault/x.md", …)` and `AppendFact("vault/x.md", …)` STILL return `ErrOwnerOnly` (the bot path never writes the now-existing vault). Stdlib-only, no-testify, matching the project test style.

- [ ] **Task 3 — The OS-enforced isolation property test, Pi-gated (`core/memory/vault_isolation_linux_test.go`, `//go:build linux`)** (AC: 2, 3)
  - [ ] A `//go:build linux` test `TestVaultIsolation_WorkerUIDDenied`: gate up front — `if runtime.GOOS != "linux" || os.Geteuid() != 0 { t.Skip("vault-isolation property is OS-enforced only on Linux as root (the Pi); skipping") }`. (The non-linux file is implicitly skipped by the build tag; add a tiny `vault_isolation_other_test.go` only if a same-named skipping test is wanted for visibility — optional.)
  - [ ] Create a `t.TempDir()` curated root, `EnsureVault` it, write `vault/secret.md` with known bytes (mode `0600`, core-owned). Pick a distinct unprivileged worker uid (default `65534`/`nobody`; resolve via `/etc/passwd` lookup or accept the conventional value — document the choice). Ensure the temp dir path is traversable enough that the *only* barrier is the `0700` vault (the test asserts the vault is the denial point, not an incidental parent-perm artifact).
  - [ ] **Child re-exec idiom (mirror 5.1's `TestMain`/`IsChild` pattern, `privsep_test.go:42-55`).** Re-exec the test binary with `SysProcAttr.Credential{Uid: workerUID, Gid: workerGID}` and a sentinel env carrying the vault path; the child attempts (a) `os.ReadFile(vault/secret.md)` and (b) `os.ReadDir(vault/)`, and reports outcomes (exit code / stdout token). The parent asserts **both** are `fs.ErrPermission` (`EACCES`) and the secret bytes never appear. A successful read (or any non-permission error that leaks bytes) FAILS the test.
  - [ ] Bound the child with a context/timeout so a hang can't wedge CI; clean teardown.

- [ ] **Task 4 — Wire vault creation in `main`, gated on uid-separation (`cmd/shelldon/main.go`)** (AC: 1, 3)
  - [ ] In the `case "privsep":` branch (`main.go:120-132`), after resolving `uid`, when `uid != 0` (and thus the drop is meaningful) call `memory.EnsureVault(filepath.Join(shelldonDir, "memory"))` and log the M3 vault creation; on error, log + degrade per existing boot-error style (do not hard-crash the pet over vault creation — surface and continue, or `os.Exit(1)` matching the sibling `OpenCurated` failure handling — match whichever the surrounding code uses for memory-open failures; `OpenCurated` currently `os.Exit(1)`s, so mirror that for consistency).
  - [ ] With `SHELLDON_WORKER` unset (default Monolith+) or `uid == 0`: create **no** vault — AD-3 keeps the vault non-existent until the worker is genuinely uid-separated. Zero behavior change to the default boot.
  - [ ] Update the `main.go:115-117` comment that currently says "Story 5.2 adds the matching vault exclusion" to reflect it now does.

- [ ] **Task 5 — Validation + regression gate** (AC: 1, 2, 3)
  - [ ] `go test -race ./...` green (the property test SKIPs on the dev/CI host with its logged reason; structural tests pass). Capture the skip line in completion notes.
  - [ ] Native build + `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build green; `golangci-lint run` → 0; `go vet` clean.
  - [ ] Unchanged/zero-diff confirmation: `worker/privsep/*` (the 5.1 transport), the `worker.Worker` interface, arbiter, dispatch, scheduler, contracts, and `curated.go`'s `vault/` rejection. `dream.go`'s `sensitiveLaneEnabled` STILL `false`.
  - [ ] **Pi proof (manual, document the result):** on the Pi as root, run the linux property test (`go test -run TestVaultIsolation ./core/memory/`) and record that it actually *executes* (not skips) and passes — the read is denied. If the Pi run is deferred, say so explicitly in completion notes (the test must not be marked "proven" while only ever skipped).

## Dev Notes

### Architecture constraints (binding)

- **AD-3 — The vault never exists until the worker is across a process wall; at M3+ `vault/` permissions exclude the worker uid (OS-enforced, not a path-filter).** This story implements the second half: "`vault/` permissions exclude the worker uid (OS-enforced, not a path-filter)." The mechanism is a `0700` core-owned dir; the proof is the uid-dropped property test. Half one (vault doesn't exist pre-M3) stays enforced by `curated.go` + `sensitiveLaneEnabled=false`. [Source: ARCHITECTURE-SPINE.md#AD-3 (lines 72-75), #Consistency-Conventions (line 159: "`vault/` … then OS-unreadable to the worker uid")]
- **NFR6 — vault isolation is OS-enforced.** The epic AC names NFR6 directly: the worker process *cannot* read the vault — kernel-denied, not Go-denied. The property test is the NFR6 evidence. [Source: epics.md#Story 5.2]
- **AD-2 / 5.1 — uid-separated recycled subprocess; the drop mechanism already exists.** `worker/privsep/cred_linux.go:applyCredential` sets `SysProcAttr.Credential{Uid,Gid}` on Linux + root + configured uid. 5.2 *reuses this exact mechanism* in the property test (re-exec a child dropped to the worker uid) — do not invent a second drop path. [Source: worker/privsep/cred_linux.go, ARCHITECTURE-SPINE.md#AD-2]
- **AD-1 / NFR3 — LLM-free core; depguard fence.** Vault creation lives in `core/memory` (core side). It must import only stdlib — no broker/provider. The property test re-execs the test binary (stdlib `os/exec` + `syscall`), pulling in nothing that breaches the core fence. [Source: ARCHITECTURE-SPINE.md#AD-1, broker/imports_test.go]
- **AD-6 — one writer (core) for memory; bot proposes only.** Core creating the empty `vault/` dir is consistent with "core is the single writer." The bot/LLM path (`curated.WriteFile`) must remain barred from `vault/`. Creating the dir does NOT open a bot write path to it. [Source: ARCHITECTURE-SPINE.md#AD-6, core/memory/curated.go]
- **AD-9 — broker is sole cred holder; vault surfacing is broker-gated.** Out of scope here: 5.2 creates an *empty isolated* vault and proves isolation. Routing sensitive learnings in (5.3) and surfacing vault contents into a prompt (broker-gated) are later. No broker interaction in this story. [Source: ARCHITECTURE-SPINE.md#AD-9]

### Key design decisions (made; flagged where genuinely forked)

- **`0700` owner-exclusion, not ACLs/xattrs.** The worker is a *different* uid; a `0700` core-owned dir excludes it by plain POSIX rules. Simplest correct OS enforcement, portable to the arm64 Pi, nothing to configure. `os.Chmod` after `MkdirAll` pins the mode past umask.
- **Vault creation is core-direct + gated on `uid != 0`, NOT via `curated.WriteFile`.** Keeps the disjoint-writer invariant intact (bot never touches the vault) and honors AD-3's "vault gated on the worker being uid-separated." Default boot (Monolith+ / no worker uid) creates no vault.
- **Property test is Pi-only by necessity; skips loudly elsewhere.** Real uid-drop needs Linux + root. The structural test (perms + disjoint-writer) runs everywhere to guard the wiring on the laptop; the OS-enforcement assertion runs only where the OS can enforce it. A skip with a reason — never a silent pass — so "proven" is honest. (Same split 5.1 used for the drop.)
- **Re-exec the test binary, dropped to the worker uid (mirror 5.1's `TestMain`/`IsChild`).** The standard Go "re-exec self + env sentinel" idiom, now with `SysProcAttr.Credential` to drop. No second binary, no fixtures.
- **Worker uid default `65534` (nobody) for the test.** Conventional unprivileged uid present on the Pi. Document it; allow override if the Pi uses a dedicated `shelldon-worker` uid (the real production value is an ops concern, not a code constant).

### Previous story / codebase intelligence

- **5.1 left this exact debt.** 5.1 AC3 + Completion Notes: "uid-drop wired + gated, not asserted-enforced … OS-enforced read-denial is Story 5.2's property test on the Pi." `main.go:116` literally says "Story 5.2 adds the matching vault exclusion." This story closes that loop. [Source: 5-1-privsep-lite-worker-subprocess-gob-transport-swap.md AC3/Completion Notes, cmd/shelldon/main.go:116]
- **Vault rejection already tested — extend, don't duplicate.** `curated_test.go:157-162` and `:279-283` already assert `vault/` writes are rejected and nothing is created. Task 2's disjoint-writer assertion should *reuse/extend* that, asserting it STILL holds after `EnsureVault` creates the (now-existing) dir. [Source: core/memory/curated_test.go, core/memory/curated.go:63-66]
- **Dream sensitive lane stays off.** `core/dream/dream.go:34-38` `sensitiveLaneEnabled=false`; `dream_test.go:199-200` asserts no `vault/` path appears after a dream. 5.2 must not flip this — 5.3 does. The new vault dir created by `main` is *outside* the dream's `t.TempDir()` roots, so `dream_test` is unaffected. [Source: core/dream/dream.go, core/dream/dream_test.go]
- **Privsep drop mechanism to mirror.** `worker/privsep/cred_linux.go` (gating: uid configured + `Geteuid()==0`) and `privsep_test.go:42-55` (`TestMain`/`IsChild` child re-exec). The property test's child re-exec + credential drop should read like these. [Source: worker/privsep/cred_linux.go, worker/privsep/privsep_test.go]
- **main wiring point.** `cmd/shelldon/main.go:102` opens curated at `~/.shelldon/memory`; the `privsep` switch is `:119-135`. `EnsureVault` slots into the `case "privsep":` branch after `uid` is resolved. [Source: cmd/shelldon/main.go]
- **No new dependency.** `os`, `os/exec`, `syscall`, `path/filepath`, `runtime`, `io/fs` are all stdlib. No `go.mod` change. [Source: go.mod]

### Latest tech information

- No external libraries. The whole story is Go stdlib: `os.MkdirAll`/`os.Chmod` (mode pinned past umask), `os.ReadFile`/`os.ReadDir` returning `*fs.PathError` wrapping `syscall.EACCES` — assert with `errors.Is(err, fs.ErrPermission)` (the portable, version-stable check). `SysProcAttr.Credential` for the uid drop is unchanged across current Go releases. Nothing here is version-sensitive; no web research warranted.

### Project Structure Notes

- **New:** `core/memory/vault.go` (or add `EnsureVault` to an existing memory file) — the core-direct vault creator. `core/memory/vault_test.go` — structural perms + disjoint-writer (everywhere). `core/memory/vault_isolation_linux_test.go` (`//go:build linux`) — the uid-dropped property test.
- **Modified:** `cmd/shelldon/main.go` — `EnsureVault` call in the `privsep` branch + the `:115-117` comment update. Possibly extend `core/memory/curated_test.go` for the post-create disjoint-writer assertion.
- **Unchanged (must stay zero-diff):** `worker/privsep/*` (5.1 transport), `worker/worker.go`, `core/arbiter/*`, `core/dispatch/*`, `core/scheduler/*`, `contracts/*`, `curated.go`'s `vault/` rejection logic, `dream.go`'s `sensitiveLaneEnabled=false`.
- **Build tags:** the property test is `//go:build linux` so the arm64-Linux build/test path runs it and darwin dev simply doesn't compile it (or compiles a skipping stub). `core/memory/vault.go` is portable (no tag).

### References

- [Source: epics.md#Story 5.2] — the AC: vault excludes worker uid; property test proves the worker process cannot read the vault (NFR6/AD-3).
- [Source: ARCHITECTURE-SPINE.md#AD-3 (72-75), #Consistency-Conventions (159)] — vault gated on uid-separation; at M3+ `vault/` permissions exclude the worker uid, OS-enforced not path-filter, then OS-unreadable to the worker uid.
- [Source: ARCHITECTURE-SPINE.md#AD-2, worker/privsep/cred_linux.go] — the uid-drop mechanism (`SysProcAttr.Credential`, Linux+root-gated) the property test reuses.
- [Source: 5-1-…-gob-transport-swap.md AC3 + Completion Notes, cmd/shelldon/main.go:116] — 5.1 wired+gated the drop and explicitly deferred OS-enforcement proof to 5.2.
- [Source: core/memory/curated.go:63-66, curated_test.go:157-162/279-283] — the existing `vault/` disjoint-writer rejection to preserve and extend.
- [Source: core/dream/dream.go:34-38, dream_test.go:199-200] — `sensitiveLaneEnabled=false`; nothing routed to a vault (stays off; 5.3 flips it).
- [Source: worker/privsep/privsep_test.go:42-55] — the `TestMain`/`IsChild` re-exec idiom to mirror for the child uid-drop in the property test.

### Decisions to confirm (surfaced for Elliot — defaults chosen, override if you disagree)

1. **Vault-creation scope.** Default: a small core `EnsureVault` helper + wire it in `main`'s `privsep` branch (vault becomes a real, gated artifact) **plus** the property test. Alternative: property-test-only (the test fabricates its own vault; `main` untouched). **Recommend the default** — it makes the vault real and gated per AD-3, and keeps the test honest about the production path. ~½ day either way.
2. **Enforcement mechanism.** Default: plain `0700` owned by the core uid (worker is a different uid → excluded). Alternative: explicit per-uid ACL/xattr. **Recommend `0700`** — simplest correct OS enforcement, portable to arm64, exactly what AD-3 specifies. No ACLs.
3. **Property-test location + uid.** Default: `core/memory/vault_isolation_linux_test.go`, child dropped to `65534` (nobody), `t.Skip` off Linux+root. Alternative: live it in `worker/privsep`. **Recommend `core/memory`** — the vault is core-owned; the test asserts a core invariant. Confirm the Pi's intended worker uid if it isn't `nobody`.
4. **Pi proof timing.** The OS-enforcement assertion only *executes* on the Pi. Default: land the code + the skip-everywhere-else behavior now, and record an explicit Pi run result in completion notes (run it on the Pi as part of this story). If the Pi isn't available this sprint, the story ships with the test SKIPPING and that fact stated plainly — not claimed as "proven." Confirm whether a Pi run gates "done."

## Dev Agent Record

### Agent Model Used

### Debug Log References

### Completion Notes List

### File List
