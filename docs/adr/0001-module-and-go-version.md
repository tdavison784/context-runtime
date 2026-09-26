# 1. Go module path and minimum Go version

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
Date: 2026-09-25

## Context

SDD §15 item 1 requires this decision before Phase 1 (§11) exits. The
runtime is a Go library (§6) with an internal/domain package that other
internal packages depend on transitively, so the module path is load-bearing
for every import statement in the repository, and the Go version affects
which standard-library APIs (`slices`, `crypto/rand`, generics) internal/domain
already uses are guaranteed available to downstream embedders.

## Decision

- Module path: `github.com/tdavison784/context-runtime`. Root package name:
  `contextruntime` (§6: "The public API lives outside internal/ ... The root
  package re-exports domain types with type aliases").
- `go 1.26.0` as the declared minimum in `go.mod`, `toolchain go1.27.1` for
  local development. 1.26 and 1.27 are the two newest majors as of
  2026-09-25; the minimum is raised to 1.27 when 1.26 leaves support (Go
  1.28's release). `GOTOOLCHAIN=auto` lets an older local `go` binary fetch
  the pinned toolchain automatically rather than failing the build.
- No cgo in the default build (this also drives the SQLite driver choice in
  ADR 3).

Committed state matches this decision: `go.mod:1-3` already declares
`module github.com/tdavison784/context-runtime`, `go 1.26.0`, and
`toolchain go1.27.1`.

## Alternatives considered

- **Track only the single newest Go release.** Rejected: a one-version
  window forces embedders onto the latest toolchain the day it ships, which
  is a heavier constraint than a library should impose; a two-release window
  is standard library-maintenance practice and still lets internal/domain use
  current generics/slices APIs.
- **A module path under a neutral org (e.g. `github.com/context-runtime/...`).**
  Rejected for V1: no such org exists yet; renaming a Go module path after
  first tag is a breaking change for every importer, so picking the
  personal-account path now and migrating later (a documented, one-time
  break) is preferred over blocking on org setup.
- **Allow cgo for the default build.** Rejected here; the consequence for the
  store is decided in ADR 3.

## Consequences / compatibility impact

- Any future module-path change is a breaking import-path change for every
  consumer; §6's internal/domain-imports-nothing rule means the blast radius
  of a rename is every other internal package plus the root package's type
  aliases.
- Raising the Go minimum on a Go 1.28 release is a routine, low-risk `go.mod`
  bump as long as no internal package has by then depended on a 1.28-only
  API; that dependency would need its own ADR-level justification given the
  two-release policy.
- `GOTOOLCHAIN=auto` means CI and contributors do not need to pre-install
  Go 1.27.1; the first build downloads it. This must be reconciled with any
  network-restricted CI runner (flag for Phase 11 hardening).

## Tests that lock the behavior

No behavior test locks a version string; this is enforced structurally:

- CI must run `go build ./...` and `go vet ./...` with `GOTOOLCHAIN=auto`
  against the committed `go.mod`, so a version mismatch fails the build
  rather than silently compiling with a newer local toolchain.
- A required lint/CI check should fail if `go.mod`'s `go` directive drops
  below the two-newest-majors policy once Go 1.28 ships.

## Open questions

### Resolved at acceptance (2026-09-26)

- Whether to publish a stability promise (e.g. semver tags) before Phase 5's
  vertical slice, or keep the module pre-v1 (`v0.x`) through Phase 10.
  **Decision:** the module stays pre-v1 (v0.x tags only) through Phase 10; a
  stability promise is decided with ADR 14 at Phase 11 (release).
- Whether `cmd/context-runtime` (§6, optional inspection CLI) ships in the
  same module or as a separate `tools/` module to avoid pulling its
  dependencies into library consumers' builds.
  **Decision:** `cmd/context-runtime` ships in this module as long as it
  imports only this module's packages and the standard library; any command
  needing third-party dependencies goes in a nested module (precedent:
  `probes/descriptor` has its own `go.mod` so the root module gains no
  dependencies).

## Review

Scrutinized by Codex gpt-6-sol xhigh (`codex-decision-review-out.md`):
AGREE — the module matches the committed code, and Go 1.26/1.27 are the
supported major releases as of 2026-09-25. No change.
