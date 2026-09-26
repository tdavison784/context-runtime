# Descriptor probe vocabulary

Status: binding for the provider probe drafts (`docs/probes/2026-09-*-descriptor-probe.md`) and
the input format for ADR 12. Introduced for PR #4 review finding SPEC-1.1: both drafts must use
the SDD §7 `Capabilities` field names **verbatim**. The sub-field names below come from the
wording of SDD §7's comments and FR-CAP-001, so the two drafts can be merged mechanically.

## 1. Draft format

Each draft gives one Go-literal `Capabilities` value per probed model (or one value plus a
"same as X except" list). Every field carries a trailing comment that starts with an evidence
label (section 4):

```go
MinimumLength: [2]int{1023, 1029}, // OBSERVED: cache__claude-sonnet-5__C1-*.json bisection
```

The rules for every draft:

- Only the fields in sections 2 and 3 appear. A fact that fits no field goes in the draft's
  prose, not in the literal.
- The value is what the strategy layer may rely on. If a commander ruling or a missing proof
  forces a more conservative value than the observation suggests, the value is the
  conservative one. The observation goes in the comment, followed by `RULING <n>` naming the
  ruling in that doc.
- An unknown value is never left blank. It takes the fail-closed default from section 5, with
  label `ASSUMED` or `DOCUMENTED-PENDING`.

## 2. `Capabilities` fields (SDD §7, verbatim)

| Field | Type | Meaning |
|---|---|---|
| `Provider` | string | `anthropic`, `openai`, ... |
| `Model` | string | Model ID exactly as sent in requests |
| `Version` | string | The profile identity from FR-CAP-001: `<api surface>/<endpoint>/<account feature profile>/<required headers and request settings>/<model version>`. Every request setting the profile depends on appears here, e.g. `messages-beta/v1-messages/legacy-2026-08-31/thinking-binding-controls-2026-08-01+prefix_mismatch_behavior=error/claude-opus-5-5`. A request that differs in any segment is a different profile |
| `ContextWindow` | int, tokens | Maximum input tokens |
| `MaxOutput` | int, tokens | Maximum output tokens the model accepts (not a value the probe chose) |
| `Counter` | CounterID | Counter identifier: `<provider>.<endpoint>/<model>` for a provider count endpoint, or the local estimator's ID |
| `Caching` | `CachingRules` | See section 3.1 |
| `Pricing` | `Pricing` | See section 3.2 |
| `Reasoning` | `ReasoningRules` | See section 3.3 |
| `Edits` | `map[EditKind]EditSafety` | See section 6. A missing key means the edit kind is not available: the strategy treats it as `REJECTED` |
| `NativeCompaction` | bool | The provider offers compaction that the runtime can use through a protocol listed in `CompactionProtocol` |
| `CompactionInstructions` | bool | A caller-supplied summarization instruction is accepted **and** changed the summary in a fixture |
| `CompactionProtocol` | `[]CompactionProtocol` | The sanctioned protocols, from the SDD §7 comment "checkpoint/pause/verified automatic": `CHECKPOINT` (a standalone operation that returns only compaction output and generates no task actions), `PAUSE` (compaction inside an inference request that stops after compacting, before any task generation), `VERIFIED_AUTOMATIC` (compaction and continuation in one response, allowed only with the FR-MAT-005 proof that the full mandatory set stays effective). An empty list means none is sanctioned |
| `CompactionMinimumTrigger` | int, tokens | Smallest accepted trigger for provider-triggered compaction. `0` means the caller decides (a `CHECKPOINT` operation with no minimum) |
| `MandatoryPreservation` | `PreservationRules` | See section 3.4 |
| `ContextEditing` | bool | Server-side clearing (not summarizing) of earlier content is verified: it was applied in a fixture, did not invalidate retained reasoning, and matched its documented rule |
| `MidConversationSystem` | bool | A system/developer-authority message placed after the first turn is proven to carry **system authority**. That needs a fixture where it changes behavior that the same text as user content does not, with a no-message control. Acceptance by the API alone is not enough |
| `NativeMemory` | bool | Provider-side memory that persists across requests without the runtime resending it. A client-executed memory tool is `false` |

## 3. Sub-types

The field names below are the SDD §7 comment words in CamelCase. Fields marked `EXTENSION`
are proposed additions that FR-CAP-001 requires but §7 does not name. ADR 12 accepts or drops
each one, and drafts use exactly these names.

### 3.1 `CachingRules` (§7: "prefix semantics, breakpoints, minimum length, TTLs")

| Field | Type | Meaning |
|---|---|---|
| `PrefixSemantics` | string | `exact-prefix`: a byte change at position p invalidates everything from p onward. State the render order if the provider defines one, e.g. `exact-prefix tools>system>messages` |
| `Breakpoints` | int | Maximum explicit cache breakpoints per request. `0` means the provider has no caller-placed breakpoints |
| `MinimumLength` | `[2]int` | `{lo, hi}` bracket in tokens of the smallest cacheable prefix: `lo` is one more than the largest prefix observed uncached, `hi` is the smallest observed cached. If only documented: `{doc, doc}` |
| `TTLs` | `[]string` | Accepted TTL values, e.g. `"5m"`, `"1h"`, `"30m"`. A value the API rejected is not listed |
| `Automatic` | bool, EXTENSION | Cache reuse needs no caller-placed breakpoint (FR-CAP-001 "caching semantics") |

### 3.2 `Pricing` (§7: "uncached input, cache write per TTL, cache read, output")

All values are USD per million tokens at list price, standard tier. The comment cites the
pricing page URL and retrieval date. Multipliers go in comments only.

| Field | Type | Meaning |
|---|---|---|
| `UncachedInput` | float64 | Base input rate |
| `CacheWrite` | `map[string]float64` | Keyed by the TTL strings in `Caching.TTLs` |
| `CacheRead` | float64 | Cache hit rate |
| `Output` | float64 | Output rate, reasoning included |

### 3.3 `ReasoningRules` (§7: "replay required, bound to prior history")

| Field | Type | Meaning |
|---|---|---|
| `ReplayRequired` | bool | `true` if a request that omits previously returned reasoning for an open tool round is rejected. Accepted-but-degraded is `false` |
| `BoundToPriorHistory` | bool | `true` if the provider **detects** that content before a reasoning block/item changed and rejects or reports the block. It is `false` when such an edit is accepted and the stale reasoning is used without report, even though the reasoning is semantically tied to that history |
| `BoundToModel` | bool, EXTENSION | Reasoning produced by one model is rejected or dropped when replayed to another |
| `DropsReported` | bool, EXTENSION | Every provider-side drop of replayed reasoning appears in a response field under this profile's `Version` settings (e.g. Anthropic `input_transformations`) |
| `PostEditReplay` | `EditSafety` or `STALE`, EXTENSION | What happens when reasoning produced **after** an edited position is replayed, which the runtime never does (section 6, `REWRITE`). `REJECTED`, `LOSSY` (dropped by the provider), or `STALE` (accepted and used without report). `STALE` is a finding against the provider, not a usable state |

### 3.4 `PreservationRules` (§7 `MandatoryPreservation`; FR-CAP-001 "pause support, retained fields and returned blocks"; FR-MAT-005)

| Field | Type | Meaning |
|---|---|---|
| `RestorationPlacement` | `[]string` | Placements after a compaction artifact that are proven to make restored mandatory content effective, e.g. `"user"`. Proof needs a restored rule that is **absent from the compacted input** and changes behavior. A placement is listed only with that proof; system/developer placement also needs `MidConversationSystem` |
| `ReturnedBlocks` | string | What a compaction returns, e.g. `one compaction block {content, signature}` or `echoed input items + one encrypted compaction item` |
| `RetainedFields` | string | Which input items the compaction output carries verbatim, counted from a fixture (e.g. "1 of 2 input messages echoed") |
| `SummaryInspectable` | bool | The compaction artifact's content is readable text |
| `SummaryIntegrity` | string | `signed` (an edit is rejected), `unsigned` (an edit is accepted), or `opaque` (encrypted, cannot be edited meaningfully) |
| `KeptReasoningValid` | bool | Reasoning in turns kept after a compaction artifact still verifies (see `BoundToPriorHistory`) |

## 4. Evidence labels

Each field comment starts with exactly one of these labels.

| Label | Meaning | May the strategy rely on the value? |
|---|---|---|
| `OBSERVED` | A committed fixture in this repository shows it. The comment names the fixture or the probe ID | Yes, for the exact profile (`Version`) probed |
| `DOCUMENTED` | Provider documentation states it. The comment gives the URL and retrieval date | Yes, until a probe contradicts it |
| `DOCUMENTED-PENDING` | Will be sourced from documentation at the ADR 12 draft; no citation yet | No. The fail-closed default applies until the citation lands |
| `ASSUMED` | Neither observed nor documented | No. The value must be the fail-closed default |

`NOT DETERMINED` is not a label. Use `DOCUMENTED-PENDING` if documentation will settle the
question; otherwise use `ASSUMED` with the fail-closed default. `RULING <n>` may follow any label
when a commander ruling set the value.

## 5. Fail-closed defaults

| Field | Default when unproven |
|---|---|
| Any `bool` capability (`NativeCompaction`, `CompactionInstructions`, `ContextEditing`, `MidConversationSystem`, `NativeMemory`, `Caching.Automatic`, `Reasoning.DropsReported`, `PreservationRules.SummaryInspectable`, `PreservationRules.KeptReasoningValid`) | `false` |
| `Reasoning.ReplayRequired` | `true` (always replay) |
| `Reasoning.BoundToPriorHistory`, `Reasoning.BoundToModel` | `false`: the runtime cannot count on the provider to catch edits, so the adapter strips reasoning itself |
| `Edits[k]` | key absent (treated as `REJECTED`) |
| `CompactionProtocol` | empty list |
| `PreservationRules.RestorationPlacement` | empty list (native compaction then cannot satisfy FR-MAT-005) |
| `ContextWindow`, `MaxOutput` | Not usable: the adapter refuses to send until a `DOCUMENTED` value exists |
| `Caching.MinimumLength` | Forecasts treat prefixes below `hi` as uncached |
| `Caching.TTLs` | Forecasts assume no cache reuse between requests |
| `Pricing.*` | Not usable: the cost model refuses to estimate until a `DOCUMENTED` value exists |

## 6. `EditKind` (FR-CAP-002) and `EditSafety`

The seven kinds are FR-CAP-002's names, with the meanings pinned to what the probes exercised.
"Previously sent" means any byte the provider received in an earlier request of this
conversation: `system`/instructions, tool definitions, and every message or item.

| EditKind | Precise meaning | Exercised by |
|---|---|---|
| `APPEND` | New items are added after the last previously sent item; no previously sent byte changes | Anthropic R1; OpenAI R1 |
| `APPEND_SYSTEM` | `APPEND` whose new item has system or developer role (Anthropic `role: "system"` message; OpenAI `developer` input item). Its `EditSafety` covers only acceptance and the effect on reasoning and cache. Authority is `MidConversationSystem` | Anthropic R1-table and C3 step 4; OpenAI K2 |
| `ADD_DEFERRED_TOOL` | A tool definition marked deferred (not loaded until referenced) is added; existing definitions are unchanged | Anthropic (`defer_loading: true`); OpenAI: not exercised |
| `MOVE_CACHE_MARKERS` | Only caller-placed cache annotations are added, moved or removed; content is unchanged. Absent from `Edits` when `Caching.Breakpoints == 0` | Anthropic (`cache_control`); OpenAI: no such mechanism |
| `DROP_LEADING_REASONING` | Reasoning blocks/items are removed oldest-first, forming a leading run; the reasoning of the latest (possibly open) round and all other content are kept | Anthropic (first thinking block removed, later kept); OpenAI R5-drop |
| `DROP_ALL_REASONING` | Every reasoning block/item is removed, including the latest round's, while its tool calls and results stay | Anthropic R2; OpenAI R2 |
| `REWRITE` | Any other change to previously sent content: editing message text, `system`/instructions or a tool definition, reordering, removing non-reasoning items, or removing reasoning that is not a leading run. As FR-CAP-002 requires, the runtime sends a REWRITE with **all reasoning at and after the first changed position removed**, and the verdict is for that request. Replaying reasoning from after the edit is not a REWRITE; it is recorded in `Reasoning.PostEditReplay` | Anthropic RW1, RW2 (verdict), R4 (`PostEditReplay`); OpenAI R4 (`PostEditReplay`) |

`EditSafety` values (FR-CAP-002), judged on the request as the runtime would send it:

| Value | Meaning |
|---|---|
| `SAFE` | Accepted, and every reasoning block/item left in the request is still used: no provider drop is reported or implied by billing. For `DROP_LEADING_REASONING` the removed leading reasoning is a deliberate budget choice and does not make the edit LOSSY |
| `LOSSY` | Accepted, but reasoning is lost: the provider drops or ignores reasoning that was sent, or the edit removes the latest round's reasoning. `DROP_ALL_REASONING` is therefore `LOSSY` whenever it is accepted, and strategies perform it only under ALLOW_RESET (FR-ASM-011) |
| `REJECTED` | The provider returns an error, so the strategy never performs it. This includes a profile that needs the removed reasoning for structural validity (T12) |

Acceptance alone is never evidence of `SAFE` for an edit that leaves reasoning in the request.
The fixture must show that the remaining reasoning was used (billed, not dropped), or the
profile must report drops (`DropsReported`) with none reported.

## 7. Open questions (ADR 12)

Rulings from PR #4 review round 1, 2026-09-26.

1. **`APPEND_SYSTEM` while `MidConversationSystem` is false.** Ruling: the Anthropic drafts
   keep `Edits[APPEND_SYSTEM]: REJECTED` as a fail-closed policy value (the observed provider
   behavior is recorded in the comment). This stands until ADR 12 defines **capability-gated
   edit kinds**: an edit kind that needs a capability is unavailable while that capability is
   `false`, and `Edits` records only the observed provider verdict.
2. **`CompactionMinimumTrigger` with several protocols.** Ruling: ADR 12 keys the trigger by
   protocol (for example `CHECKPOINT: 0, PAUSE: 50_000` for claude-opus-5-5). Until then drafts
   record the provider-triggered minimum and name the other protocol's minimum in the comment.
3. **Proposed sub-fields.** Ruling: these five go to ADR 12 as proposals, not as settled §7
   fields:
   - `CachingRules.Automatic`
   - `ReasoningRules.BoundToModel`
   - `ReasoningRules.DropsReported`
   - `ReasoningRules.PostEditReplay`
   - `Version` used as the full FR-CAP-001 profile identity

   Drafts may use them with the names defined here. Not covered by this ruling: the section 3.4
   `PreservationRules` sub-field names are also derived (SDD §7 gives that type no comment)
   rather than verbatim.
