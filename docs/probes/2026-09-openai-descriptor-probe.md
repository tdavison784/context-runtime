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

## Reasoning binding (R1-R5)

Setup (`probes/descriptor/openai/main.go` `reasoning()`): a `high`-effort tool-call turn
(`*_r1_tool`) produces a `reasoning` item plus a `function_call`; the tool result is appended, then
each variant below replays that history with a `medium`-effort continuation. Both models gave
identical qualitative results; token counts differ slightly by model.

**R1 — replay reasoning unchanged: ACCEPTED.**
`gpt_6_astra_r1_replay.json` replays the tool-turn `reasoning` item and result verbatim and
succeeds (`status` implied completed, no `error`), with
`usage: {input_tokens: 112, output_tokens: 21, output_tokens_details.reasoning_tokens: 13}` and the
correct final answer (`"cobalt"`, matching the fixed-up tool result `"A = cobalt"`). Verdict:
**SAFE** (FR-CAP-002 `APPEND`-class edit).

**R2 — drop the reasoning, keep the tool call/result: ACCEPTED, but not verbatim replay.**
`gpt_6_luna_r2_drop.json` / `gpt_6_astra_r2_drop.json` strip every `type: "reasoning"` item from
history while keeping the `function_call`/`function_call_output` pair, and the request still
succeeds: `usage: {input_tokens: 71, output_tokens: 47, reasoning_tokens: 39}`, output types
`["reasoning", "message"]`, correct answer `"cobalt"`. The provider does not require the original
reasoning block to accompany its own tool call — it silently regenerates fresh reasoning
(`reasoning_tokens: 39`, close to the original turn's 39) rather than rejecting the request.
Verdict: **LOSSY**, not REJECTED — this is `DROP_ALL_REASONING`/`DROP_LEADING_REASONING` in
FR-CAP-002 terms, and it is accepted with silent regeneration rather than an error.

**R3 — modify reasoning content/signature: REJECTED.**
`gpt_6_astra_r3_corrupt.json` flips one byte of the tool-turn reasoning item's
`encrypted_content` and gets a 400:
```
"message": "The encrypted content for item rs_0e1a837efed155ff016ab7c08642ec87d197604182ade7125e could not be verified. Reason: Encrypted content could not be decrypted or parsed.",
"type": "invalid_request_error",
"code": "invalid_encrypted_content"
```
Identical for `gpt_6_luna_r3_corrupt.json`. Verdict: **REJECTED**, confirming the encrypted
reasoning blocks are integrity-checked server-side (a `REWRITE` of the reasoning block itself is
always rejected, never silently dropped).

**R4 — edit an earlier message while replaying later reasoning unchanged: ACCEPTED.**
`gpt_6_astra_r4_edit_earlier.json` rewrites the first user message's `content` (asking for lookup
code B instead of A) while leaving the tool-turn `reasoning`/`function_call`/`function_call_output`
items byte-for-byte unchanged, and still succeeds:
`usage: {input_tokens: 90, output_tokens: 23, reasoning_tokens: 15}`, answer `"cobalt"` (the value
fixed by the unedited tool result, not a value implied by the edited instruction — the model is not
observed to notice the edit; it just reads the tool result back). Verdict: **SAFE** — the API's
reasoning-item integrity check is not bound to the preceding conversation text, only to the
reasoning item's own encrypted content (contrast with R3, where corrupting that same field is
rejected). This means a `REWRITE` of content *before* an open reasoning/tool round is accepted by
this provider as long as the reasoning items themselves are untouched — narrower than FR-CAP-002's
default assumption that any REWRITE drops trailing reasoning; the descriptor should record this as
model-specific rather than assume it generalizes.

**R5 — reasoning from turns before the last user message: replay is optional, not required.**
Both variants append a new final user turn ("What was the lookup word? One word.") after the full
r1 history and succeed:
- `gpt_6_astra_r5_keep_older.json` (reasoning items kept): `usage: {input_tokens: 125,
  output_tokens: 6, reasoning_tokens: 0}`, output types `["message"]` only — no new reasoning was
  generated; the model answered directly from the retained tool result.
- `gpt_6_astra_r5_drop_older.json` (reasoning items stripped): `usage: {input_tokens: 84,
  output_tokens: 22, reasoning_tokens: 14}`, output types `["reasoning", "message"]` — the model
  generated fresh reasoning instead.

Both give the correct answer (`"cobalt"`). Verdict: reasoning predating the last user message is
**not required** to be replayed (SAFE to drop); keeping it does not force the model to reproduce or
re-emit it either. This is consistent with R2's LOSSY-not-REJECTED finding.

**Stateless `previous_response_id` chaining: REJECTED (not a reasoning-binding question per se, but
observed incidentally).** `gpt_6_astra_stateless_previous.json` sends `store: false` on the first
call, then tries `previous_response_id` on the second, and gets:
```
"message": "Previous response with id 'resp_0e1a837efed155ff016ab7c084a82887d1821f5984226b74ab' not found.",
"code": "previous_response_not_found"
```
By contrast, `gpt_6_astra_state_first.json`/`gpt_6_astra_state_previous.json` (`store: true`)
chain successfully: the second call's `usage.input_tokens` (25) is far smaller than the full
resend would cost, implying server-side state reuse, and returns the correct answer (`"cobalt"`).
Implication for the descriptor: `previous_response_id` continuation requires `store: true`;
`store: false` responses are not retrievable by ID even moments later.
