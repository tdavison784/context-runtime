# 13. Deployment model

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
Date: 2026-09-25

## Context

SDD §15 item 13 asks: "Deployment model: embedded library, sidecar process,
or both." §6 already states the intended shape ("Use Go ... alignment with
the surrounding agent infrastructure, and easy Temporal or Kubernetes
integration later") and that the public API lives outside `internal/` so
other modules can import it. §10 requires mutations to serialize per
session because WORKFLOW/SESSION/AGENT items are shared across tasks. ADR 3
requires the SQLite file be owned by one process (0600 permissions).

## Decision

- V1 ships as an **embedded Go library**: package `contextruntime` at the
  module root, imported directly by a harness process. An optional
  `cmd/context-runtime` inspection CLI (§6) ships in the same module for
  operator/debugging use (`Inspect`, per the `Runtime` interface in §8), not
  as a service.
- A sidecar/`contextd` process behind a language-neutral wire protocol is
  explicitly **post-V1** (§2's non-goals implicitly, and §8: "The first
  public API is Go methods in package contextruntime; a wire protocol is
  deferred").
- Stores are single-process: the SQLite file is owned by exactly one OS
  process. `store.Store.Update` (`internal/store/store.go`) documents
  per-session write serialization; V1 implements that serialization as an
  in-process mutex combined with SQLite's own `BEGIN IMMEDIATE` (ADR 3),
  not as a distributed lock. This means exactly one process may open a
  given session's database file for writing at a time.

## Alternatives considered

- **Sidecar process now, embedded later.** Rejected: §8 defers the wire
  protocol explicitly, and a sidecar's principal-authentication story (who
  is the trusted caller across a process boundary?) is unresolved — FR-DOM-003
  and FR-AUTH-001 assume the embedding process authenticates principals and
  never trusts model-authored session/agent IDs (§8: "The embedding process
  constructs principals and authenticates ownership claims"); a sidecar
  needs its own transport-level authentication design that V1 does not need
  to solve to ship a useful library.
- **Multi-process access to one SQLite file (e.g. several harness workers
  sharing a session's database).** Rejected: WAL mode with one writer
  connection (ADR 3) does not extend to multiple *processes* safely without
  additional coordination (advisory locks, a lock file, or a supervising
  process); ADR 3's "one writer connection" assumption is process-local.
  Multi-process write sharing is deferred to whatever sidecar design
  eventually replaces this ADR's scope.
- **Postgres-backed multi-process V1.** Rejected: FR-PER-001 defers Postgres
  until after SQLite acceptance; building multi-process support around a
  store that doesn't exist yet is out of order.

## Consequences / compatibility impact

- Every harness integration in V1 is a Go program (or a Go program with
  cgo-free FFI bindings) that imports this module directly; there is no
  network boundary to design defenses for yet, which simplifies §9's threat
  model to "a trusted embedding process, untrusted content within it" rather
  than also "a trusted embedding process across an untrusted network."
- A future sidecar will need its own ADR revisiting principal
  authentication, transport security, and the `Store` interface's
  process-locality assumptions (`BEGIN IMMEDIATE`, in-process mutex) since a
  sidecar could serve multiple embedding processes concurrently in ways this
  ADR does not need to handle.
- `cmd/context-runtime` must not become a load-bearing service; it is
  explicitly an inspection tool per §6 and §13's "optional executable and
  inspection commands," so it must not gain write paths that bypass the
  library's own `Runtime` interface (§8).

## Tests that lock the behavior

- Required: a test (or documented manual check) asserting the SQLite store
  refuses or safely handles a second process attempting to open the same
  database file for writing concurrently — at minimum, that SQLite's own
  locking prevents corruption; ideally that the second opener gets a clear
  error rather than a `busy_timeout` stall indistinguishable from a hang.
- `internal/store/storetest.Run` (the shared conformance suite, run by both
  `internal/store/memory:TestConformance` and `internal/store
  /sqlite:TestConformance`) is the primary lock on `Store` interface
  behavior regardless of deployment; no test targets multi-process access,
  since it remains out of scope for V1.
- No test locks "no wire protocol exists" directly; this is enforced by the
  absence of a server/RPC package in `internal/` (structural, not test-driven).

## Open questions

### Resolved at acceptance (2026-09-26)

- What the eventual sidecar's authentication mechanism will be (mTLS,
  Unix-socket peer credentials, a bearer token minted by the embedding
  process) — deferred, but worth flagging now since `Principal` construction
  (`internal/domain/principal.go`) already assumes a trusted caller and
  will need a distinct "who constructed this principal, and do we trust
  them" story once a process boundary exists.
  **Decision:** sidecar authentication is out of V1 scope and will get its
  own ADR if a sidecar is built; the embedded library trusts its embedding
  process to construct principals (SDD section 8).
- Whether `cmd/context-runtime` ships in this module or a separate `tools/`
  module (also raised in ADR 1) — relevant here because a sidecar
  executable, if built later, would likely also live under `cmd/`.
  **Decision:** same as ADR 1: `cmd/context-runtime` ships in this module as
  long as it imports only this module's packages and the standard library;
  any command needing third-party dependencies goes in a nested module.

## Review

Scrutinized by Codex gpt-6-sol xhigh (`codex-decision-review-out.md`):
AGREE — the embedded-library choice does not conflict with the SDD, with
the caveat (already covered by ADR 3's consequences) that SQLite's
transaction constraints must still protect a database opened by more than
one process. No change.
