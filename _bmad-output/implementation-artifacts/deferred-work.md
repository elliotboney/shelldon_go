# Deferred Work

## Deferred from: code review of 1-1-versioned-contracts-gob-round-trip (2026-06-20)

- **AllKinds mutability and Kind-AllKinds sync gap** — unsure which fix to take (unexported+Kinds() vs exported+comment); revisit when a second Kind is added.

- **gob type names include module path** — if module is forked/renamed, existing gob blobs produce "type not registered"; no test guards this. File: `contracts/register.go`.
- **Header.V is defined but nothing reads or gates on it** — intentional per architecture; version negotiation is future work. File: `contracts/envelope.go`.
- **No negative test for gob type-not-registered path** — future bus code should verify the error is catchable rather than a panic. File: `contracts/register.go`.
- **nil Payload in Envelope is untested** — gob behavior with nil interface field is unvalidated; relevant when bus enforces non-nil before encoding. File: `contracts/contracts_test.go`.
