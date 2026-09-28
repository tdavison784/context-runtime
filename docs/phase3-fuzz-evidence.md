# Phase 3 fuzz evidence (gate item 14, TEST-1.3)

Bounded fuzz runs of the Phase 3 fuzz targets, with the exact commands and
results. FuzzParse (directive) already had a checked-in corpus from earlier
runs.

## Local campaign (2026-09-27, branch p3fix/w7)

The environment was go1.27.1 darwin/arm64 with 12 CPUs. Each command ran
from the repository root.

| Target | Command | Duration | Execs | New interesting (cache total) | Result |
|---|---|---|---|---|---|
| FuzzIngest | `go test -run='^$' -fuzz='^FuzzIngest$' -fuzztime=60s -parallel=4 ./internal/ingest` | 60s | 300,120 | 16 (837) | PASS, no crasher |
| FuzzPolicyAgreement | `go test -run='^$' -fuzz='^FuzzPolicyAgreement$' -fuzztime=60s -parallel=4 ./internal/directive` | 60s | 6,926,533 | 19 (551) | PASS, no crasher |
| FuzzMatchClaim | `go test -run='^$' -fuzz='^FuzzMatchClaim$' -fuzztime=60s -parallel=4 ./internal/obligation` | 60s | 2,218,909 | 0 (19) | PASS, no crasher |
| FuzzTestsPassVerdict | `go test -run='^$' -fuzz='^FuzzTestsPassVerdict$' -fuzztime=60s -parallel=4 ./internal/obligation` | 60s | 2,097,287 | 1 (13) | PASS, no crasher |
| FuzzKeyedWriteInputs | `go test -run='^$' -fuzz='^FuzzKeyedWriteInputs$' -fuzztime=60s -parallel=4 ./internal/tools` | 60s | 969,993 | 6 (72) | PASS, no crasher |

The SEC round-1 review independently ran all six targets, FuzzParse
included, for 75s each at `-parallel 4` and found no crashers.

## Seed corpora

`internal/<pkg>/testdata/fuzz/<Target>/` holds an evenly size-spaced sample
of each campaign's interesting inputs, at most 48 per target. Plain
`go test ./...` replays them as seeds, and so does CI's test step. The
corpora are replayed with this command:

```
go test -race -count=1 -run '^(FuzzIngest|FuzzPolicyAgreement|FuzzParse|FuzzMatchClaim|FuzzTestsPassVerdict|FuzzKeyedWriteInputs)$' ./internal/ingest ./internal/directive ./internal/obligation ./internal/tools
```

## CI

The `fuzz` job in `.github/workflows/ci.yml` runs every fuzz target for 30s
per push and pull request (`go test -run='^$' -fuzz='^<Target>$'
-fuzztime=30s <pkg>`). `TestEveryFuzzTargetRunsInCI` (internal/ingest)
fails when a `Fuzz*` target in the module is missing from that job.
