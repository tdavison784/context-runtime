# Context Runtime: Spec Driven Development

Status: proposed  
Version: 0.3  
Date: 2026-09-25

## 1. Purpose

Context Runtime is a provider-aware context runtime for LLM agents. It sits between an agent harness and an LLM provider and manages semantic memory, task state, provenance, memory lifecycle, and model-specific context optimization. Garbage collection, memory, and provider optimization are features; the product is the context control plane.

Most harnesses treat conversation history, agent memory, and the next model context as the same thing. This runtime keeps them separate:

- Audit history: everything that happened. The transcript is immutable.
- Semantic state: what the agent currently knows, is trying to accomplish, and must keep satisfying.
- Model context: what one particular model call receives.

A smaller prompt is not automatically cheaper, faster, more correct, better cached, or compatible with the model's earlier reasoning. Rebuilding context from scratch can destroy prompt-cache reuse, invalidate reasoning continuity, duplicate provider-native compaction, and raise uncached-token cost. The runtime therefore optimizes effective task execution, not token count.

This specification is the V1 implementation contract: behavior, interfaces, invariants, persistence, testing, and delivery gates. Appendix A is non-normative.

### Architecture

    harness events
          │
          ▼
    semantic runtime        what the agent knows: items, authority, lifecycle,
          │                 provenance, supersession, obligations, archive
          ▼
    context planner         what this call needs: the Context Plan
          │
          ▼
    materialization         how to give it to this model: append or rebase,
    strategy                native operations, cost estimate
          │
          ▼
    provider adapter        serialization, token counting, transport
          │
          ▼
    provider                model-specific execution: caching, reasoning,
                            compaction

The semantic layer (runtime and planner) is provider-independent and never reads provider capabilities. The materialization layer (strategies and adapters) decides representation only and never changes semantic state. Strategies work with provider-native caching, reasoning continuity, compaction, and context editing rather than against them.

### Terminology

- Model call: one provider request produced by one assembly.
- Turn: begins when a USER event is ingested for a task and ends when the next USER event is ingested for that task. A turn usually contains many model calls. TURN scope and TTLTurns count turns.
- Pending input: the ingested items the next model call must answer: the latest user message, or the tool results that complete the open tool round.
- Exchange group: items a provider requires to travel together: an assistant message, the tool calls it contains, their tool results, and any provider reasoning items attached to that assistant message.
- Open tool round: an exchange group whose tool results have not yet been sent to the model.
- Semantic state: items, relationships, lifecycle state, and obligations. It is provider-independent.
- Context Plan: the planner's provider-independent statement of what the next model call needs (FR-PLN-001).
- Materialization: turning a Context Plan into a provider request.
- Capability descriptor: versioned data describing a (provider, model): limits, pricing, caching, reasoning rules, safe history edits, and native context features (FR-CAP-001).
- Epoch: a run of model calls for one task, provider, and model during which the rendered request history is append-only. An epoch starts with a rebase, where materialization selects context from scratch.
- Semantic pressure: active semantic memory above a policy threshold. Provider pressure: the rendered request approaching the usable budget.
- Effective task cost: the currency cost of completing a task (FR-COST-001).
- Principal: the session, workflow, task, and agent a caller acts for, plus the caller's authority. Every API call carries one.
- Sequence number: a per-session, monotonically increasing number assigned when an event commits. It is the runtime's logical clock. Wall-clock time is recorded for audit only.
- Token counter: the component that counts serialized provider input for a (provider, model) pair. A counter is exact, or estimating with a declared upper error bound.

## 2. Goals and non-goals

### Goals

- Parse authorized context directives from user and harness input.
- Preserve source authority and prevent lower authority from promoting itself.
- Track goals, constraints, knowledge, evidence, state, obligations, provenance, and supersession separately from the raw provider conversation.
- Produce a provider-independent Context Plan for every model call and materialize it with a strategy chosen from provider capabilities.
- Minimize effective task cost, not input tokens, while preserving goals, constraints, required state, reasoning continuity, and correctness.
- Produce requests that are structurally valid for the target provider and never edit history in ways the provider marks unsafe.
- Stay within a strict usable token budget.
- Archive and retrieve context without destroying audit history.
- Explain every semantic and provider decision.
- Survive restart and concurrent ingestion.
- Replay recorded sessions across strategies, simulate their cost, and evaluate against raw-transcript and provider-native baselines.
- Provide fake, OpenAI-compatible, and Anthropic-compatible provider adapters.

### V1 non-goals

An AI operating system, Temporal scheduler, Kubernetes controller, Vicegerent integration, vMCP, distributed context service, custom model proxy, Rust engine, vector database, custom tokenizers, and UI or dashboard are deferred. Also out of scope for V1:

- Cross-session memory. No item is visible outside its session (INV-05).
- Runtime-generated summaries. The runtime stores and uses summary items supplied by the harness or agent with provenance (FR-REL-002); core never calls a model. Provider-native compaction runs on the provider, through a strategy (FR-MAT-002).
- LLM-based classification, relationship detection, or obligation matching.
- Content redaction and deletion. Stores are treated as secret-bearing (section 9).

Interfaces may leave room for these, but V1 must prove runtime semantics first. The expected post-V1 direction is a context service (contextd) behind a language-neutral Context Protocol, so agent frameworks in Go, Python, and TypeScript can share the runtime.

## 3. Functional requirements

Requirement IDs are stable and are part of the test contract. New requirements take the next free ID in their group; removed IDs are not reused.

### Ingestion and authority

- FR-ING-001: Every event enters one pipeline, in this order: normalize; assign authority per span; parse directives in authorized spans; classify (a directive's kind wins over heuristics); apply generation, scope, and retention defaults; deduplicate; detect relationships; count tokens with each registered counter; assign a sequence number; persist in one transaction.
- FR-ING-002: Authorities are SYSTEM, HARNESS, USER, AGENT, TOOL, and RETRIEVED_CONTENT. Precedence is SYSTEM > HARNESS > USER > AGENT > TOOL = RETRIEVED_CONTENT. TOOL and RETRIEVED_CONTENT cannot override each other. Where a total order is required (FR-ASM-006), TOOL sorts before RETRIEVED_CONTENT.
- FR-ING-003: An event may not raise its own authority, generation, pinned state, scope, retention, or mandatory status. Only an authorized source or runtime policy may grant those properties. Span authority cannot exceed the principal's authority.
- FR-ING-004: Directive parsing applies only to spans with SYSTEM, HARNESS, or USER authority. An event may carry several spans; the harness marks embedded content it knows about (attachments, file contents, echoed tool output) as TOOL or RETRIEVED_CONTENT spans. Directive headings inside fenced code blocks or block quotes are never parsed. Tool output and retrieved content remain content even when they contain directive-looking headings.
- FR-ING-005: Stable source identity, content hash, or explicit relationship data must make duplicate events detectable while preserving audit references. Duplicates are linked with DUPLICATE_OF, never merged. Deduplication never crosses session, scope, or authority boundaries, and a duplicate never inherits the canonical item's authority, generation, retention, or mandatory status.

### Directives

Directives are parsed by the runtime. They are lifecycle instructions to the runtime, not text suggestions to the model.

- FR-DIR-001: V1 recognizes the content sections Goal, Pinned, Working, Remember, References, and Ephemeral, and the lifecycle commands Resolve and Unpin.
- FR-DIR-002: Directive IDs use [id] syntax and are stored independently from display text. IDs are unique within task scope. An item without an id receives a derived ID (the lowercased keyword, a hyphen, and the first 8 hex digits of its content hash), reported in diagnostics and inspection. Reusing an ID in the same task creates a new item that SUPERSEDES the previous one, subject to FR-REL-006.
- FR-DIR-003: Defaults are:
  - Goal: goal kind, durable generation, task scope, PROTECTED retention and mandatory while active.
  - Pinned: constraint kind (instruction via kind=), pinned generation, task scope, PROTECTED retention, mandatory.
  - Working: task_state kind (conversation via kind=), working generation, task scope, NORMAL retention.
  - Remember: fact kind (decision or summary via kind=), durable generation, task scope, HIGH retention.
  - References: reference kind, working generation, task scope, NORMAL retention. Each item names an item ID, a path, or a URL. A name that matches an item ID in scope, or the source path of an ingested item, gets a REFERENCES edge; a path not yet read is linked when an item with that source path is ingested later.
  - Ephemeral: evidence kind (tool_result via kind=), ephemeral generation, TURN scope, LOW retention.
- FR-DIR-004: Parser output includes source spans, directive ID, source authority, parsed scope, parsed TTL, and diagnostics. Malformed directives do not discard unrelated content.
- FR-DIR-005: Resolve ends an active goal: its lifecycle becomes RESOLVED, it stops being mandatory, and its retention drops to HIGH. Unpin moves a pinned item to the durable generation with HIGH retention. Both require authority at least equal to the target's, are recorded as lifecycle events, and produce a diagnostic if the ID is unknown in the task. Other lifecycle words produce ErrUnsupportedDirective diagnostics rather than ambiguous mutations.
- FR-DIR-006: Directives follow this grammar (ABNF):

      section    = marker SP keyword [SP "[" id "]"] *(SP attr) EOL body
      marker     = 1*6"#"
      keyword    = "Goal" / "Pinned" / "Working" / "Remember" / "References"
                 / "Ephemeral" / "Resolve" / "Unpin"
      body       = item-list / text   ; up to the next heading of the same or higher level
      item-list  = 1*item
      item       = bullet SP ["[" id "]" SP] ["{" attr *(SP attr) "}" SP] text *continuation
      bullet     = "-" / "*" / 1*DIGIT "."
      id         = 1*64(ALPHA / DIGIT / "-" / "_" / ".")
      attr       = attr-name "=" value
      attr-name  = "kind" / "scope" / "ttl" / "obligation"
      value      = 1*(ALPHA / DIGIT / "-" / "_" / ".")

  A section whose body is a top-level list yields one item per list item; indented continuation lines belong to their item. Otherwise the body is one item and may take its id from the heading; a heading id on a list section is a diagnostic. Section attributes apply to every item, and item attributes override them. Keywords match case-insensitively; everything else is exact. A heading whose first word is not a keyword is ordinary content.

  Attributes are allowed as follows: kind on Pinned, Working, Remember, and Ephemeral; scope on all content sections; ttl (a positive count of turns) on Working, Remember, References, and Ephemeral; obligation on Pinned. scope= may widen scope beyond TASK only in SYSTEM or HARNESS spans. Resolve and Unpin take target IDs from the heading or from list items with no text. Unknown attributes, invalid values, and disallowed attributes produce diagnostics and are ignored.

  Directive text is never interpreted as policy. A lifecycle hint about other content, such as "build output can be discarded after validation", is stored as an item and does not change GC behavior in V1.

  Example:

      ## Goal
      Upgrade Foo to v2 while maintaining backwards compatibility.

      ## Pinned
      - [api] Do not modify exported APIs.
      - [tests] {obligation=tests_pass} All tests must pass.
      - [architecture] Read docs/architecture.md.

      ## Working
      - Investigating internal/client.go.
      - Current issue is TestLegacyClient.

      ## Remember
      - Foo v2 requires context.Context.
      - [retry] {kind=decision} Keep the v1 retry policy.

      ## References
      - docs/architecture.md
      - go.mod

      ## Ephemeral ttl=2
      - (pasted build output)

      ## Unpin
      - [architecture]

  Phase 2 adds a canonical example set under testdata/directives/ that is part of the test contract.

### Domain model

- FR-DOM-001: Every item has an immutable ID; session, workflow, task, and agent IDs; kind, generation, authority, scope, lifecycle state, retention class, content parts, content hash, per-counter token counts, sequence number, audit timestamps, usage counters, source reference, and optional event and directive IDs.
- FR-DOM-002: Kinds include goal, constraint, instruction, user_message, assistant_message, conversation, fact, decision, task_state, tool_call, tool_result, error, artifact, reference, summary, evidence, and reasoning. A reasoning item is an opaque provider reasoning block, bound to the provider, model, and epoch that produced it. Obligations are companion records (FR-OBL-001), not items.
- FR-DOM-003: Scopes are TURN, TASK, WORKFLOW, SESSION, and AGENT, with TURN ⊂ TASK ⊂ WORKFLOW ⊂ SESSION. Visibility requires matching session IDs plus: TURN, same task and current turn; TASK, same task; WORKFLOW, same workflow; SESSION, any principal in the session; AGENT, same agent. This matrix is the V1 default; ADR 6 confirms it before Phase 1 exits.
- FR-DOM-004: Generations are PINNED, DURABLE, WORKING, and EPHEMERAL. An item is pinned exactly when its generation is PINNED; there is no separate flag. Generation drives retention policy, not how a provider receives the item. Promotion and demotion are explicit, audited transitions.
- FR-DOM-005: Lifecycle state is ACTIVE, RESOLVED (goals only), or ARCHIVED, independent of generation. A durable item may be archived and a working item may remain active. Superseded and rehydrated are derived from relationships and retrieval events, not stored.
- FR-DOM-006: Evidence, knowledge, and state are separate semantic categories. Derived knowledge points to evidence; state changes point to the observations or decisions that caused them. For example, a 30K-token compiler output (evidence) supports the fact "the FooClient constructor now requires context.Context" (knowledge, DERIVED_FROM the output), which drives the task state "3 call sites need migration". The output can archive, the fact stays durable, and the task state stays active until superseded. Kinds map to categories:
  - Directive: goal, constraint, instruction.
  - Knowledge: fact, decision, summary, reference.
  - Evidence: tool_result, error, evidence, artifact. The evidence kind covers evidence not produced by a tool, such as a harness assertion or a user-supplied log.
  - State: task_state.
  - Conversation: user_message, assistant_message, conversation, tool_call, reasoning.
- FR-DOM-007: Mandatory status is derived at planning time, never stored. An item is mandatory when it is system policy (a SYSTEM-authority instruction), a security constraint (a pinned constraint with SYSTEM or HARNESS authority), part of the pending input or its exchange group, an active goal, pinned, or an UNRESOLVED or BLOCKED obligation (unless FR-OBL-003 policy disables it).
- FR-DOM-008: Content, kind, authority, IDs, scope, source, and sequence number are immutable. Generation, lifecycle state, retention class, usage counters, and version change only through audited lifecycle events.

### Provenance, supersession, and obligations

- FR-REL-001: Relationships support DERIVED_FROM, SUPERSEDES, DEPENDS_ON, REFERENCES, SATISFIES, and DUPLICATE_OF. Relationship records are the only store of edges; items do not duplicate them.
- FR-REL-002: A derived item cannot be persisted with missing required provenance. Dangling relationship IDs are rejected with ErrDanglingRelationship.
- FR-REL-003: Supersession marks obsolete state so it is no longer current truth, while retaining both nodes and the edge for audit and replay. For example, test states "29 failing", "7 failing", "1 failing", and "PASS" form a SUPERSEDES chain, and only PASS is current. When superseded content leaves the rendered request is a materialization decision (FR-MAT-004).
- FR-REL-004: Supersession is acyclic. A cycle is a transaction error.
- FR-REL-005: A “why do we believe this?” query traverses provenance to evidence under the same authorization and scope checks. Nodes the principal cannot see are omitted and the path is marked truncated.
- FR-REL-006: A SUPERSEDES edge requires the superseding item's authority to be at least the superseded item's. A violating edge fails with ErrInvalidAuthorityPromotion.
- FR-REL-007: V1 creates relationships only from explicit relationship data on the event, directive ID reuse (FR-DIR-002), and deterministic policy rules. The V1 rule set: a tool_result DEPENDS_ON the tool_call it answers, and a tool_result from the same tool with the same canonicalized arguments in the same task supersedes the earlier result. Rules are versioned with the policy.
- FR-OBL-001: An obligation has a stable ID, description, status, scope, owner/source, creation time, matcher, and satisfying evidence IDs. In V1, an obligation is created from a Pinned item with obligation=<matcher>, from a HARNESS event that declares one, or when a registered matcher's deterministic claim pattern matches a Pinned item's text. For example, the file_read matcher claims "Read <path>" and is satisfied by a read of that path; tests_pass claims "All tests must pass" and is satisfied by a passing test run. An explicit attribute wins over claim patterns. The runtime never uses a model to infer obligations.
- FR-OBL-002: Statuses are UNRESOLVED, SATISFIED, BLOCKED, and WAIVED. Status history is append-only. Allowed transitions:
  - UNRESOLVED to SATISFIED: the obligation's matcher accepts evidence, or a HARNESS assertion. Satisfying evidence IDs are recorded.
  - SATISFIED to UNRESOLVED: automatic when satisfying evidence is superseded by evidence the matcher rejects, or by HARNESS assertion.
  - UNRESOLVED to BLOCKED and back: HARNESS or USER.
  - Any status to WAIVED: a principal with authority at least the obligation's source authority. WAIVED is terminal.

  AGENT, TOOL, and RETRIEVED_CONTENT cannot change obligation status. Other transitions fail with ErrInvalidTransition.
- FR-OBL-003: Unresolved and blocked obligations are mandatory task state unless authorized policy disables obligation materialization.
- FR-OBL-004: Tool evidence satisfies an obligation only through its deterministic matcher or an authorized harness assertion. Matchers and their claim patterns are registered by name and versioned with the policy.

### Assembly

Assembly is planning (what the call needs) followed by materialization (how to send it).

- FR-ASM-001: Assemble(principal, request) takes the pending input item IDs, provider and model, token budget, reserved output tokens, and the harness's tool definitions. It returns normalized messages, selected item IDs, assembly ID, strategy, epoch ID, mode (REBASE or APPEND), requested native operations, token accounting, estimated cost, and the decision trace. Pending input must already be ingested.
- FR-ASM-003: The usable budget is min(token budget, model context window) minus reserved output tokens, reasoning headroom, and the counter's safety margin. The counter registered for the target (provider, model) counts every serialized input, including system content, tool definitions, message framing, and pending input. Assembled input tokens, as counted, never exceed the usable budget. An estimating counter's safety margin must cover its declared error bound.
- FR-ASM-004: Mandatory items are never silently discarded or truncated. If they exceed budget, return ErrMandatoryContextExceedsBudget with required/available counts and item IDs, so the caller can Unpin, Resolve, or split the task.
- FR-ASM-007: Every assembly records a decision trace. It holds a semantic decision for every candidate (included/excluded, a reason code from a closed set, total score and per-factor contributions, generation, authority, token count, rehydrated, superseded, and mandatory fields) and the provider decisions from materialization (strategy, mode, native operations, reason codes that cite capability descriptor fields, and the estimated cost of each alternative considered).

### Context planning

- FR-PLN-001: Before every model call, the planner produces a Context Plan from semantic state: the pending input, mandatory items, relevant items (ranked), optional items (ranked), retrievable archived items worth a stub, and obsolete items (superseded or expired) that may still be in the rendered history. The plan states what should be available, not how to render it.
- FR-PLN-002: The planner is provider-independent. It does not read capability descriptors or the provider conversation, so the same semantic state produces the same plan for every provider.
- FR-PLN-003: Relevant items clear the policy's relevance threshold. Optional items fall below it; a strategy may include them when budget and cost allow.
- FR-ASM-002: The planner partitions candidates into mandatory items (FR-DOM-007) and non-mandatory items. All mandatory items are included or assembly fails (FR-ASM-004). Non-mandatory items are ranked by score (FR-ASM-005); generation contributes to the score but is not a hard tier. Policy v1 weights generation so that, other factors equal, active task state ranks above durable knowledge, then working memory, rehydrated items, and ephemeral evidence. Selection order never determines render order (FR-RND-001).
- FR-ASM-005: Non-mandatory ranking uses an explainable score defined by the versioned policy. Factors: explicit importance, authority, generation, relevance, dependency value, recurrence, recency, state value, token cost, rehydration cost, duplication, and supersession. V1 relevance is lexical overlap with the pending input and active goals, using the normalization from ADR 7. Recency uses sequence numbers and call indexes, never wall-clock time. Scores are fixed-point integers.
- FR-ASM-006: Ties break by score (descending), authority (descending, FR-ING-002 order), last-used call index (descending), sequence number (ascending), then item ID (ascending).
- FR-ASM-008: Session, task, agent, and scope filters run before scoring. Cross-session items never enter the candidate set.

### Provider capabilities

- FR-CAP-001: Each (provider, model) has a versioned capability descriptor covering: context window and output limits; token counting method; prompt caching rules (prefix semantics, breakpoint limits, minimum cacheable length, TTLs); pricing (uncached input, cache write per TTL, cache read, output); reasoning rules (whether reasoning must be replayed and whether it is bound to prior history); the safe history edits; native compaction and whether it accepts instructions; context editing and tool-result clearing; mid-conversation system messages; and provider memory.
- FR-CAP-002: Safe history edits are an explicit set, not a single flag: APPEND, APPEND_SYSTEM, DROP_LEADING_REASONING, MOVE_CACHE_MARKERS, and REWRITE (any other change to earlier content). A strategy may perform only the edits the descriptor lists.
- FR-CAP-003: Descriptors are data, versioned, and selected per request. Provider- and model-specific checks live only in descriptors and adapters, never in the semantic runtime or planner.
- FR-CAP-004: Adapter contract tests verify each descriptor against its provider: edits marked unsafe are rejected or drop reasoning, stable prefixes produce cache reads, and declared native operations work. They run in CI against recorded fixtures and periodically against live providers.

Informative example, verified 2026-09: Claude Fable 5.1 and Claude Opus 5.5 bind thinking blocks to the conversation before them. Editing, reordering, or removing earlier turns invalidates every later thinking block, and accounts created on or after 2026-08-31 get a 400 for it. Appending, appending system messages, dropping a leading run of thinking blocks, moving cache markers, and server-side compaction or context editing are safe. Their descriptors list APPEND, APPEND_SYSTEM, DROP_LEADING_REASONING, and MOVE_CACHE_MARKERS, but not REWRITE.

### Materialization

- FR-MAT-001: A materialization strategy takes the Context Plan, the capability descriptor, and the task's provider conversation (the current epoch's rendered history). It returns a materialization plan: mode (APPEND or REBASE), the rendered request, native operations to request, an estimated cost, the alternatives it considered with their estimated costs, and a provider decision trace.
- FR-MAT-002: V1 provides one strategy family parameterized by rebase policy. Its named configurations are:
  - Rebuild: rebase every call, materializing minimal context from semantic state. Suited to providers with weak caching, REWRITE-safe history, and no history-bound reasoning.
  - AppendOnlyCached: a stable prefix and append-only history within an epoch. It rebases only on the required triggers in FR-ASM-010 or when FR-COST-003 favors a rebase. This is the default for providers with prompt caching or history-bound reasoning.
  - ProviderNative: append-only; under provider pressure it requests the provider's native compaction or context editing instead of rebasing. It requires policy opt-in, because provider-generated compaction content cannot be replayed offline.
- FR-MAT-003: Policy lists the strategies allowed for each (provider, model); when several are allowed, the cost model chooses (FR-COST-002). A strategy never requests a native operation the descriptor does not declare and never performs an edit outside the descriptor's safe set.
- FR-MAT-004: Semantic decisions (archive, supersede, expire) do not by themselves remove bytes from the next request. The strategy decides whether each takes effect at the next rebase, through a native operation, or not within the epoch.
- FR-MAT-005: After a provider-native compaction, the strategy appends the current mandatory set (goals, pinned items, unresolved obligations) as system content, so retention never depends on the provider's summary. Where the provider's compaction accepts instructions, the strategy also passes the mandatory set as retention instructions.
- FR-MAT-006: A ProviderNative strategy sets the provider's compaction trigger below the usable budget, so INV-01 holds.
- FR-ASM-009: Packing works on exchange groups: a group is included or excluded as a unit. An included item's required DEPENDS_ON targets are included too, or the item is excluded.
- FR-ASM-010: Materialization runs in epochs. In APPEND mode, the rendered request is the previous request in the epoch, unchanged except for safe edits, plus the items ingested since; decisions are recorded for the new items only. A rebase is required when the task has no epoch; the provider, model, or tool definitions change; a SYSTEM or HARNESS item arrives and the adapter has no append-only form for it; or appending would exceed the usable budget and no declared native operation resolves it. A strategy may also rebase when the cost model favors it (FR-COST-003). Rebuild rebases on every call.
- FR-ASM-011: Reasoning items are replayed verbatim within their epoch and never after a rebase. A rebase should happen only when no tool round is open. If one is forced while a round is open, the open round's reasoning is dropped and a reasoning_dropped event is recorded.

### Cost model

- FR-COST-001: Effective task cost is uncached input + cache writes + cache reads + output + retrieval + provider compaction + context-management overhead, in currency, priced from the capability descriptor.
- FR-COST-002: Strategy and rebase choices minimize estimated effective cost subject to hard constraints: mandatory items represented, only safe edits, the usable budget respected, and reasoning continuity preserved where the descriptor marks it required. Latency, pruning regret, and cache disruption are reported and affect decisions only through explicit policy thresholds; they are never added to currency.
- FR-COST-003: When no rebase is required, a strategy rebases only if the estimated cost of the next K calls after rebasing (one cache write for the new prefix, then cache reads) is lower than the cost of continuing (cache reads on the growing prefix plus uncached appended tokens). K is set by policy (ADR 15). The estimate is deterministic given the plan, descriptor version, and counts.
- FR-COST-004: Each materialization records its estimated cost, and the provider's actual usage is recorded when the response arrives, so estimate error is measured.

### Rendering

- FR-RND-001: At a rebase, render order is fixed and independent of selection order: (1) system content: system policy, security constraints, active goals, pinned requirements, and unresolved obligations, in sequence order; (2) a context section with the selected non-mandatory items grouped by category, in sequence order; (3) the conversation: selected exchange groups in chronological order, ending with the pending input. Items added during an epoch are appended where they occur and move to their section at the next rebase.
- FR-RND-002: Authority maps to provider roles without escalation. SYSTEM and HARNESS render as system content, or as provider mid-conversation system messages where supported; USER as user; AGENT as assistant; TOOL as tool results; RETRIEVED_CONTENT as delimited data inside user or tool-result content. TOOL, RETRIEVED_CONTENT, and embedded spans render inside adapter-defined delimiters labeled with their authority. Occurrences of the runtime's delimiters or section headers inside untrusted content are escaped.
- FR-RND-003: Every rendered request satisfies the target provider's structural rules, including message ordering, tool call and result pairing, and reasoning placement. Assembly fails with ErrInvalidProviderRequest rather than return an invalid request.
- FR-RND-004: At a rebase, items in the plan's retrievable set may render as stubs (ID, kind, short description, token size) within a policy stub budget, so the model can request them (FR-RET-006).

### Garbage collection, retrieval, and persistence

GC operates on semantic state. It decides whether evidence is still active, whether state is obsolete or superseded, whether an item can archive or be retrieved later, and whether it is mandatory. It does not decide what bytes the next request carries (FR-MAT-004).

- FR-GC-001: GC runs on semantic pressure (task active tokens above a policy threshold, expressed as a fraction of the usable budget), task completion, supersession, TTL expiration, explicit lifecycle command, or policy request.
- FR-GC-002: GC archives by default. Automatic deletion is outside V1.
- FR-GC-003: Pinned items and active goals survive every automatic GC path, including TTL expiration. Superseded state may leave active semantic memory but remains auditable.
- FR-GC-004: Stale, duplicated, superseded, and unused ephemeral items are preferentially collectible.
- FR-GC-005: GC is transactional and idempotent.
- FR-GC-006: GC changes reach the rendered request only through the strategy (FR-MAT-004).
- FR-RET-001: The archive supports Search, Get, and Rehydrate. Each call carries a principal and applies the same scope checks as assembly.
- FR-RET-002: V1 search works without a vector database through a deterministic lexical/indexed implementation.
- FR-RET-003: Rehydration records the principal, triggering actor, query, item IDs, latency, success/failure, and assembly usage.
- FR-RET-004: Repeated successful rehydration increases retention priority through an explainable recurrence signal. Rehydrations of one item within an epoch count once, and the signal has a policy cap, so the model cannot raise an item's retention without limit.
- FR-RET-005: A pruning-regret event occurs when an item excluded or archived at a rebase is rehydrated before the next rebase in the same task. Regret metrics report event count, rehydrated tokens, model calls between exclusion and rehydration, and failed or slow rehydrations.
- FR-RET-006: The runtime provides provider-neutral tool definitions and handlers for archive search and rehydration that the harness may register with the model. The rehydrate handler returns the item's content as the tool result, so it enters the conversation without a rebase. Rehydrated items become ACTIVE and are protected from automatic GC for a policy-defined number of model calls.
- FR-PER-001: V1 provides in-memory and SQLite stores; Postgres may implement the same interface after SQLite acceptance.
- FR-PER-002: Persistence covers items, relationships, generations, authority, scope, lifecycle state, obligations and their transitions, lifecycle events, epochs, assembly records and decision traces, provider strategy decisions, retrieval events, token counts, provider usage, and cost and cache observations.
- FR-PER-003: Restart reconstructs the same logical state and relationships.
- FR-PER-004: Mutations use transaction protection and preserve graph integrity under concurrent ingestion.

### Providers

- FR-PROV-001: Core uses a normalized provider interface for messages, responses, and usage metadata.
- FR-PROV-002: V1 includes deterministic fake, OpenAI-compatible, and Anthropic-compatible adapters. The OpenAI-compatible adapter also serves local models behind OpenAI-compatible servers. Adapters are stateless: they send the full rendered request and never rely on provider-side conversation state. ADR 9 fixes the OpenAI API surface.
- FR-PROV-003: Strategies and adapters cannot change semantic state, and adapters cannot change strategy decisions. Provider-native context management is used only through a strategy the descriptor supports (FR-MAT-003), and each use is recorded on the assembly.
- FR-PROV-004: Each adapter provides a serializer, a token counter, and capability descriptors for its models. Serializers and counters implement interfaces defined in internal/domain, so strategies do not import adapters.
- FR-PROV-005: Adapters return provider reasoning blocks as reasoning items for ingestion; replay follows FR-ASM-011.
- FR-PROV-006: Adapters report provider usage, including cache-read and cache-write input tokens and reasoning drops where the provider reports them.

### Observability, replay, and simulation

- FR-OBS-001: Emit structured events for ingestion, parsing, relationships, transitions, GC, retrieval, rehydration, planning, strategy selection, epochs and rebases, native operations, provider calls, obligations, and errors.
- FR-OBS-002: Report correctness signals (constraint violations, lost goals, lost obligations), context size (raw transcript tokens, active semantic memory, provider-visible tokens, peak context), cache behavior (cached, uncached, cache-write, and cache-read tokens; hit ratio; invalidation events; stable-prefix length), cost (input, output, and cache cost; cost per task and per successful task), memory management (archives, rehydrations, repeated rehydrations, supersessions, promotions, demotions), reliability (pruning regret, rehydration failures, provider compatibility errors such as rejections or reasoning drops caused by runtime edits, assembly failures, counter error), and latency (planning, retrieval, materialization, provider, and total task).
- FR-OBS-003: Sessions are recorded as provider-independent events: user inputs, agent outputs, tool calls and results, semantic events, provider calls and responses, cost and cache metadata, and the expected final state. Every assembly is replayable from this ordered log together with the policy version, descriptor versions, counter identity, and budget.
- FR-OBS-004: Identical inputs, policy, descriptors, and counters produce identical plans, decisions, and rendered output.
- FR-OBS-005: Offline replay runs a recorded session through each strategy with the fake provider and the simulator, holding recorded model responses fixed. Live evaluation runs the benchmark against real models for each mode (section 12).
- FR-SIM-001: The strategy simulator takes a recorded session and a capability descriptor and estimates, per strategy: cache hits and misses, cached and uncached tokens, cache writes, compactions, retrievals, context pressure, and cost. It is deterministic.
- FR-SIM-002: The simulator models provider-native compaction with a policy-configured size model; it does not predict compaction content.
- FR-SIM-003: Live runs periodically validate the simulator, and the difference between simulated and actual cost and cache usage is reported.

## 4. Non-functional requirements

Values are proposed and are confirmed at Phase 11.

- NFR-001: Assemble p95 latency (planning plus materialization) is at most 100 ms with 10,000 candidate items in one task on the SQLite store, excluding remote token counting.
- NFR-002: Ingest p95 latency is at most 20 ms per event, excluding remote token counting.
- NFR-003: Remote counting is called at most once per item per counter (counts are cached by content hash), plus at most one whole-request count per assembly.
- NFR-004: The benchmark reports store growth per model call, including assembly records and decision traces.

## 5. Hard invariants

- INV-01: Assembled input tokens, as counted by the target's registered counter, never exceed the usable token budget.
- INV-02: Mandatory context never disappears silently.
- INV-03: Pinned directives and active goals survive automatic GC.
- INV-04: Lower-authority content cannot promote itself or override higher-authority context, including through supersession, deduplication, or obligation transitions.
- INV-05: Cross-session items, and items outside the principal's task, workflow, or agent visibility, never enter plans, assembly, retrieval, explanation, or inspection results.
- INV-06: Supersession graphs are acyclic.
- INV-07: Required derived items retain valid provenance.
- INV-08: Archived items remain auditable and rehydratable.
- INV-09: Given caller-supplied stable event IDs, identical inputs produce deterministic plans, decisions, and rendered output.
- INV-10: Restart and concurrent mutation preserve valid state and relationships.
- INV-11: Every rendered request is structurally valid for its target provider.
- INV-12: Within an epoch, each rendered request is the previous one plus appended content, changed only by edits in the target's safe edit set.
- INV-13: No request replays reasoning items outside the epoch that produced them, and no strategy requests a native operation the target does not declare.

## 6. Proposed Go package boundaries

Use Go for fast iteration, strong concurrency, clear interfaces, a good fit for long-running runtime services, alignment with the surrounding agent infrastructure, and easy Temporal or Kubernetes integration later. Do not use Rust until profiling shows a real need.

The public API lives outside internal/ so harnesses in other modules can import it. Keep dependency direction one way:

    .                     package contextruntime: public Runtime interface, request and response types, errors
    internal/domain       pure types, enums, transitions, validation, plan types, serializer and counter interfaces
    internal/directive    source-gated markdown parser and diagnostics
    internal/store        store interfaces, memory store, SQLite store
    internal/graph        provenance and supersession operations
    internal/policy       scoring, retention, authority, lifecycle policy, relationship rules
    internal/ingest       normalization, classification, relationship detection
    internal/obligation   matchers, claim patterns, creation, and satisfaction
    internal/lifecycle    semantic GC, TTL, promotion, demotion, archive eligibility
    internal/plan         context planner: filtering, mandatory partition, scoring, plans
    internal/capability   versioned capability descriptors and pricing
    internal/strategy     materialization strategies, epochs, packing, rendering sections, cost model
    internal/retrieve     archive search, rehydration, retrieval tool handlers
    internal/provider     adapters: serializers, counters, transports
    internal/telemetry    events, metrics, explanations
    internal/replay       session recorder, replay runner, strategy simulator, baseline comparison
    cmd/context-runtime   optional executable and inspection commands

internal/domain imports no other internal package. internal/plan does not import internal/capability, internal/strategy, or internal/provider; a lint check enforces the split between what and how. internal/provider and internal/telemetry import only internal/domain. The root package re-exports domain types with type aliases. Policy and planning functions should be pure where possible for property testing and replay.

## 7. Normative data model

The exact representation may evolve, but these semantics are required. Tests validate behavior, not struct layout.

    type ContextItem struct {
        ID           string
        EventID      string            // caller-supplied stable event ID, when present
        DirectiveID  string
        Seq          uint64            // per-session commit order; the logical clock
        SessionID    string
        WorkflowID   string
        TaskID       string
        AgentID      string
        Kind         ContextKind
        Generation   Generation        // PINNED is the only pinned marker
        Authority    Authority
        Scope        Scope
        Lifecycle    Lifecycle         // ACTIVE, RESOLVED, ARCHIVED
        Retention    RetentionClass    // PROTECTED, HIGH, NORMAL, LOW
        Parts        []ContentPart     // text, plus image and document references
        ContentHash  string
        Tokens       map[CounterID]int
        Importance   int64             // fixed-point
        CreatedAt    time.Time         // audit only; never used for scoring
        LastUsedCall uint64
        AccessCount  int
        TTLTurns     *int
        Tags         []string
        Source       *SourceRef
        Version      uint64
    }

    type ContextPlan struct {
        Pending     []ItemRef
        Mandatory   []ItemRef
        Relevant    []ScoredItem
        Optional    []ScoredItem
        Retrievable []ItemRef          // archived items worth a stub
        Obsolete    []ItemRef          // superseded or expired, possibly still rendered
        Decisions   []PlanDecision
    }

    type Capabilities struct {
        Provider, Model, Version string
        ContextWindow            int
        MaxOutput                int
        Counter                  CounterID
        Caching                  CachingRules    // prefix semantics, breakpoints, minimum length, TTLs
        Pricing                  Pricing         // uncached input, cache write per TTL, cache read, output
        Reasoning                ReasoningRules  // replay required, bound to prior history
        SafeEdits                []EditKind
        NativeCompaction         bool
        CompactionInstructions   bool
        ContextEditing           bool
        MidConversationSystem    bool
        NativeMemory             bool
    }

    type MaterializationStrategy interface {
        Materialize(ctx context.Context, plan ContextPlan, caps Capabilities, conv Conversation) (MaterializationPlan, error)
    }

Mandatory, superseded, and rehydrated status are derived (FR-DOM-005, FR-DOM-007). Edges live only in Relationship records (FR-REL-001).

Required companion records are Relationship, Obligation, ObligationTransition, LifecycleEvent, Epoch, AssemblyRecord, AssemblyDecision, ProviderDecision, RetrievalEvent, ProviderUsage, and CostEstimate.

## 8. Public service contract

The first public API is Go methods in package contextruntime; a wire protocol is deferred. Every method takes the caller's principal explicitly:

    type Principal struct {
        SessionID  string
        WorkflowID string
        TaskID     string
        AgentID    string
        Authority  Authority
    }

    type Runtime interface {
        Ingest(ctx context.Context, p Principal, event Event) ([]ContextItem, error)
        Plan(ctx context.Context, p Principal, req PlanRequest) (ContextPlan, error)
        Assemble(ctx context.Context, p Principal, req AssembleRequest) (Assembly, error)
        Search(ctx context.Context, p Principal, req SearchRequest) ([]SearchHit, error)
        Get(ctx context.Context, p Principal, id string) (ContextItem, error)
        Rehydrate(ctx context.Context, p Principal, req RehydrateRequest) (ContextItem, error)
        Collect(ctx context.Context, p Principal, req CollectRequest) (CollectReport, error)
        ExplainAssembly(ctx context.Context, p Principal, assemblyID string) (AssemblyRecord, error)
        Provenance(ctx context.Context, p Principal, id string) (ProvenanceGraph, error)
        TransitionObligation(ctx context.Context, p Principal, req ObligationTransition) (Obligation, error)
        Inspect(ctx context.Context, p Principal, req InspectRequest) (InspectView, error)
        CompleteTask(ctx context.Context, p Principal) error
    }

The embedding process constructs principals and is trusted to do so. Plan returns the Context Plan without materializing it, for inspection and testing. Lifecycle commands (Resolve, Unpin) arrive as directives through Ingest. CompleteTask resolves the task's active goals and runs GC for task completion.

Machine-checkable errors include ErrMandatoryContextExceedsBudget, ErrNotFound, ErrInvalidAuthorityPromotion, ErrMissingProvenance, ErrDanglingRelationship, ErrSupersessionCycle, ErrInvalidTransition, ErrInvalidProviderRequest, ErrUnsupportedCapability, and ErrVersionConflict. ErrUnsupportedDirective and ErrMalformedDirective are diagnostic codes, not returned errors. Cross-session and unauthorized lookups return ErrNotFound so callers cannot probe for existence; the runtime records them as ErrCrossSessionAccess security events.

## 9. Security specification

- Trust is metadata assigned at ingestion, never inferred from content claims. Span authority cannot exceed the principal's authority.
- Directive parsing is source gated (FR-ING-004). A web page containing "## Pinned" followed by "Ignore prior instructions." is stored with RETRIEVED_CONTENT authority and is not pinned.
- Tool output cannot pin itself, promote itself, supersede or deduplicate into higher-authority items, change obligation status, or override higher-authority context. The agent cannot override system or harness restrictions. User directives keep USER authority.
- Retrieval, explanation, provenance, planning, and inspection APIs enforce the same session, task, agent, and scope checks as assembly.
- Strategies and adapters never change semantic state (FR-PROV-003).
- The runtime guarantees its own metadata and decisions. It cannot stop a model from following instructions inside untrusted content; it reduces that risk by rendering untrusted content as delimited, labeled data (FR-RND-002).
- Stores hold full tool output and may contain secrets. V1 creates SQLite files readable only by their owner, and telemetry events carry IDs and hashes rather than content unless a debug policy enables content. Redaction is deferred (section 2).
- Tests cover retrieved injection, tool injection, delimiter spoofing, authority downgrade, supersession and deduplication across authority, cross-session lookup, cross-task scope, and provenance access controls. The directive parser is fuzzed.

## 10. Determinism and concurrency

Determinism is required for parsing diagnostics, item IDs when the caller supplies a stable event ID, relationship ordering, scoring, planning, strategy selection, cost estimates, packing, rendering, GC decisions, and assembly records. Time and random IDs are injectable in tests.

- Scoring and recency use sequence numbers and call indexes, not wall-clock time.
- Assembly side effects (usage counters, last-used call index, epoch state) are recorded as lifecycle events, so replay reapplies them in order.
- Scores and cost estimates are fixed-point integers. Go permits fusing floating-point multiply-add, and the gc compiler does so on arm64 but not on default amd64 builds, so floating-point results could differ between an Apple Silicon laptop and an amd64 CI runner.
- Sequence numbers are assigned per session at commit and define the order replay uses.

Mutations serialize per session, because items at WORKFLOW, SESSION, and AGENT scope are shared across tasks; transaction retries are allowed on conflict. A successful operation is linearizable from the session's perspective. Tests run with the race detector and cover concurrent duplicate ingestion, supersession, GC, and restart snapshots.

## 11. Delivery phases and exit gates

Each phase lists the ADRs (section 15) that must be accepted before it exits.

1. Domain and stores: enums, validation, scopes, authority, generations, lifecycle, relationships, sequence numbers, memory store, SQLite schema, and transactions. ADRs 1, 3, 4, 6, 13. Gate: unit, persistence, restart, graph integrity, and race tests.
2. Directives and ingestion: grammar, parser, IDs, defaults, diagnostics, normalization, spans, classification, deduplication, and relationship rules. Gate: canonical directive examples, parser fuzzing, and injection tests.
3. Semantic state engine: current truth, supersession, derived facts, obligations with matchers and transitions, promotion and demotion, TTL, and archive eligibility. ADR 8. Gate: pinned and goal protection, auditability, and obligation transitions including reversion when evidence is superseded.
4. Context planner: filtering, mandatory partition, scoring, Context Plans, and semantic decision records. ADRs 5, 7. Gate: plan determinism property tests and the planner import check.
5. Capabilities and materialization framework: capability descriptors, the strategy interface, counter and serializer interfaces, rendering, exchange groups, the cost model, and a fake adapter that enforces ordering and tool-pairing rules at least as strict as the real providers. ADRs 2, 11. Gate: budget and validity property tests, the overflow error, and deterministic cost estimates.
6. Rebuild strategy. Gate: stays within budget, renders valid requests, and renders identical requests for identical semantic state.
7. AppendOnlyCached strategy: epochs, stable prefix, reasoning continuity, and the rebase rule. ADR 15. Gate: append-only and prefix-stability property tests, rebase-versus-continue cost tests, and no reasoning replayed across rebases.
8. Provider adapters and native context management: Anthropic-compatible and OpenAI-compatible adapters, descriptor verification, and the ProviderNative strategy. ADRs 9, 12. Gate: adapter contract and golden serialization tests, unsupported native operations never attempted, and the mandatory set re-appended after compaction.
9. Archive and retrieval: lexical search, retrieval tools, stubs, rehydration, retention feedback, and regret. Gate: archive recovery and explainable priority change.
10. Replay, simulation, and evaluation: session recorder, offline replay across strategies, strategy simulator, and runners for benchmark modes A through D. ADR 10. Gate: all modes complete and emit metrics, and the simulator is validated against a live sample.
11. End-to-end and hardening: the coding-agent workload on Claude, an OpenAI-compatible model, and the fake or local provider where practical, plus security review and profiling. ADR 14. Gate: hard invariants pass, NFR targets are met, and success criteria are reported.

## 12. Test specification

Test classes: unit, property and invariant, fuzz, integration, provider compatibility, cache simulation, replay, concurrency, restart durability, security, and end-to-end.

### Unit tests

Cover directive grammar, defaults, and diagnostics; authority ordering; generation and lifecycle transitions; kind classification; relationship validation and rules; provenance traversal; supersession cycles; obligation transitions and claim patterns; score calculation; stable ties; plan construction; capability selection; cost estimation; budget overflow; token accounting; epoch and rebase triggers; rendering and escaping; search; rehydration; and regret metrics.

### Required behavior tests

- Semantic: pinned requirements never disappear; goals remain active until resolved; ephemeral information can archive; supersession selects current truth; derived facts retain provenance; rehydration restores evidence; repeated rehydration affects retention; obligations transition correctly.
- Authority: retrieved content cannot pin itself; tool output cannot elevate authority; the agent cannot override system or harness restrictions; user directives keep user authority.
- Provider: an append-only strategy never rewrites prior history; the Rebuild strategy stays within the budget; capability selection chooses a supported strategy; unsupported native operations are never attempted; reasoning-bound history is never edited outside the safe set; native compaction is selected when policy allows it and the cost model favors it.
- Cache: stable-prefix preservation is measurable; equivalent semantic state does not invalidate the cache; the cost model recognizes when a rebase costs more than cached continuation.
- Isolation: session A's context never appears in session B; task-scoped memory expires correctly.

### Property tests

Prove that assembly never exceeds the usable budget, every rendered request is structurally valid for its provider, requests within an epoch change only by appends and safe edits, plans are provider-independent, GC never archives a pinned item or active goal automatically, no plan or assembly contains another session’s items, supersession remains acyclic, derived items have valid provenance, identical seeded inputs plan and assemble identically, and valid event replay reconstructs the same state.

### Fuzz tests

Fuzz the directive parser and the renderer's escaping with Go native fuzzing.

### Integration tests

Prove SQLite restart recovery, schema migrations, concurrent ingestion integrity, authority preservation through storage and assembly, fake-provider end-to-end flow, archive/rehydration, obligations, and provider adapter contracts with golden serialized requests and descriptor verification. Run all concurrency tests under the race detector.

### End-to-end benchmark

Use a deterministic coding-agent workload: upgrade a dependency, read architecture requirements, inspect source, run tests, hit failures, inspect docs and the changelog, modify code, rerun tests, resolve all failures, and produce a final result. The workload generates substantial tool output, compiler output, test output, file reads, searches, and obsolete state.

Run the workload in four modes:

- Mode A, raw transcript: the traditional growing history. When it exceeds the context window, the oldest exchange groups are dropped and each drop is recorded.
- Mode B, minimal rebuild: the runtime with the Rebuild strategy.
- Mode C, provider native: the provider's caching, compaction, and context editing, without the semantic runtime.
- Mode D, Context Runtime: the semantic runtime with the cost-selected provider-aware strategy.

The runtime must be shown to improve on provider-native behavior (Mode C), not only on a naive baseline. On providers with strong caching and history-bound reasoning, Mode D is expected to match Mode C on cost and improve correctness by keeping mandatory state across compactions and recovering archived evidence; the benchmark tests that hypothesis. Offline replay and the simulator compare strategies on fixed recorded sessions; live runs compare modes end to end. Each live mode runs enough times to report a correctness pass rate with a confidence interval, and ADR 10 sets the run count and the correctness oracle.

Success criteria for Mode D:

- Same or better task correctness than Modes A and C.
- Effective cost per task and per successful task no higher than Modes A and C.
- Zero lost pinned constraints, zero lost goals, zero cross-session leakage, zero invalid provider requests, zero budget violations (checked against provider-reported input tokens), and zero provenance corruption.
- Zero unsafe history edits and zero reasoning rejections or drops caused by runtime edits.
- Bounded semantic working memory, recoverable archived evidence, and inspectable decisions.

Token reduction is reported for every mode but is not a success criterion.

## 13. Inspection contract

Inspect exposes a text or structured view, similar to a /context command in coding harnesses, with the active goal, pinned items and obligation statuses, working items, durable items, archived counts, token usage, the current strategy and epoch, recent GC and provider decisions, cache statistics, and unresolved requirements. Inspection is read-only and uses runtime authorization and scope checks.

## 14. Acceptance checklist

V1 is accepted only when:

- Authorized directives are parsed per the grammar and persisted with source metadata.
- Goals, constraints, and obligations are maintained, and Resolve and Unpin release goals and pins.
- Current state is tracked independently from transcript history, with supersession and provenance queryable through Get, Search, and Provenance.
- Goals and pinned requirements survive every automatic GC path.
- Authority boundaries hold: tool and retrieved content cannot create privileged directives or override higher-authority items through supersession, deduplication, or obligations.
- Unresolved obligations are visible, evidence-backed satisfaction works, and superseded evidence reverts satisfaction.
- Archived evidence is searchable and rehydratable by the harness and through the retrieval tools.
- Context Plans are provider-independent and deterministic.
- Provider capabilities are modeled as versioned descriptors, verified by adapter contract tests.
- The runtime selects among Rebuild, AppendOnlyCached, and ProviderNative strategies, preserves append-only history when the descriptor requires it, and rebuilds when the cost model favors it.
- Provider-native context management works through adapters, with the mandatory set kept across compactions.
- Mandatory context never silently disappears, the token budget is never exceeded, and every rendered request is valid for its provider.
- Cache behavior, estimated and actual cost, and pruning regret are tracked.
- Every semantic and provider decision is explained.
- Session and task isolation is enforced.
- Restart and concurrent operations preserve valid state.
- Offline replay runs recorded sessions across strategies, and the simulator is validated against live runs.
- The end-to-end workload completes, and Mode D meets the success criteria in section 12.
- Unit, integration, property, fuzz, security, concurrency, replay, and end-to-end tests pass.

## 15. ADRs required before affected phase exits

Section 11 maps each ADR to the phase it gates.

1. Go module path and minimum Go version.
2. Token counter strategy per provider and model: exact or estimating counters, error bounds, safety margins, and use of provider count endpoints.
3. SQLite driver and migration mechanism.
4. Stable ID format and event ID requirements.
5. Scoring policy v1: factor weights, fixed-point scale, relevance threshold, GC pressure threshold, and stub budget. (Replaces "Whether Pinned always implies Mandatory", which FR-DIR-003 and FR-DOM-007 now decide.)
6. Scope containment matrix (confirms FR-DOM-003).
7. Lexical index and normalization rules.
8. Obligation matchers, claim patterns, and false-positive handling.
9. Provider transport libraries, retry policy, and the OpenAI API surface.
10. Benchmark fixture, correctness oracle, and run count.
11. Render templates and delimiters per provider.
12. Capability descriptor format and verification schedule, provider reasoning handling (including rebases during an open tool round), and native context management settings.
13. Deployment model: embedded library, sidecar process, or both.
14. Content redaction and retention after V1.
15. Cost model parameters: the rebase horizon K and the simulator's compaction size model.

Each ADR records the decision, alternatives, compatibility impact, and tests that lock the behavior.

## Appendix A: Development tooling (non-normative)

Use the current flagship reasoning model, GPT-6 Astra, with high reasoning effort for architecture, implementation, debugging, and long-horizon coding. Use extra-high reasoning selectively for security review, replay correctness, and final hardening when evaluation shows a measurable benefit. Use the balanced or efficient model tiers for routine mechanical work after the contract and tests exist.

This workload needs judgment across a large design surface, tool use, deterministic implementation, and failure analysis. Current official OpenAI guidance recommends Astra for ambitious deliverables and complex reasoning, recommends high reasoning for complex agentic workflows, and recommends extra-high for long-running agentic tasks. This guidance will age and does not constrain the runtime, which remains provider independent.

Sources: [OpenAI model selection](https://developers.openai.com/api/docs/guides/model-selection), [OpenAI reasoning models](https://developers.openai.com/api/docs/guides/reasoning).
