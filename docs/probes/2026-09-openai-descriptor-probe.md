# 2026-09 OpenAI descriptor probe

Phase 1 exit gate (SDD.md section 11, phase 1): "A throwaway live probe of the ... OpenAI
descriptor assumptions (reasoning binding, cache reads, compaction protocol) runs during this
phase so ADR 12 rests on observed behavior before the vertical slice." This document answers the
probe brief's R1-R5 (reasoning binding), C1-C4 (cache reads), K1-K3 (compaction protocol), and
counter/model-list questions from the sanitized fixtures committed under
`probes/descriptor/testdata/openai/`, and closes with a draft `Capabilities` descriptor per model
for ADR 12.

Evidence standard: every claim below cites a fixture file and quotes the observed field or error
verbatim. Where the committed fixtures do not settle a question, it is marked **NOT DETERMINED**
rather than inferred.

## Method

- Code: `probes/descriptor/openai` (nested Go module, `github.com/openai/openai-go/v3 v3.66.0`),
  driven by the OpenAI Responses API (`/responses`, `/responses/input_tokens`,
  `/responses/compact`, `/models`).
- Models probed: `gpt-6-astra` (current flagship reasoning model) and `gpt-6-luna` (cheaper
  tier), selected from the 132 IDs returned by `/models` (`models.json`) — the `gpt-6-*` family is
  the newest available, alongside an unprobed `gpt-6-sol`.
- Run: 2026-09-26 (fixture `created_at`/`completed_at` epoch times resolve to that date, e.g.
  `probes/descriptor/testdata/openai/gpt_6_astra_k1_compact.json` `created_at: 1790427089` =
  2026-09-26 12:51 UTC).
- Fixtures are sanitized per the probe brief: `encrypted_content`, `signature`, `id`, `call_id`,
  and `previous_response_id` values are replaced with `{length, sha256}`; `organization`,
  `project`, `user`, and `safety_identifier` are redacted. No headers, keys, or account
  identifiers are present (verified with
  `git diff | grep -iE "sk-|api[_-]?key|bearer|authorization|org-|proj_"`, no matches).
- No additional API calls were made in this write-up session; all answers below come from the
  fixtures already committed. One question (C4 pricing multipliers) is answered from public OpenAI
  documentation instead, since the fixtures only show the accepted/rejected TTL *values*, not
  pricing.
