# Context Runtime: Spec Driven Development

Status: proposed  
Version: 0.2  
Date: 2026-09-25

## 1. Purpose

Context Runtime is a model independent runtime between an agent harness and an LLM provider. It constructs a bounded working-memory view from the complete transcript, harness state, tool activity, and retrieved content.

The transcript is the immutable audit history. Runtime state determines what is active, archived, rehydrated, superseded, or unresolved for each model invocation.

This specification is the V1 implementation contract: behavior, interfaces, invariants, persistence, testing, and delivery gates. Appendix A is non-normative.

### Terminology

- Model call: one provider request produced by one assembly.
- Turn: begins when a USER event is ingested for a task and ends when the next USER event is ingested for that task. A turn usually contains many model calls. TURN scope and TTLTurns count turns.
- Pending input: the ingested items the next model call must answer: the latest user message, or the tool results that complete the open tool round.
- Exchange group: items a provider requires to travel together: an assistant message, the tool calls it contains, their tool results, and any provider reasoning items attached to that assistant message.
- Open tool round: an exchange group whose tool results have not yet been sent to the model.
- Epoch: a run of model calls for one task, provider, and model during which the rendered request history is append-only. An epoch starts with a rebase, where assembly selects context from scratch.
- Principal: the session, workflow, task, and agent a caller acts for, plus the caller's authority. Every API call carries one.
- Sequence number: a per-session, monotonically increasing number assigned when an event commits. It is the runtime's logical clock. Wall-clock time is recorded for audit only.
- Token counter: the component that counts serialized provider input for a (provider, model) pair. A counter is exact, or estimating with a declared upper error bound.

## 2. Goals and non-goals

### Goals

- Parse authorized context directives from user and harness input.
- Preserve source authority and prevent lower authority from promoting itself.
- Track goals, constraints, knowledge, evidence, state, obligations, provenance, and supersession.
- Assemble deterministic context within a strict usable token budget.
- Produce requests that are structurally valid for the target provider and keep the serialized prefix stable within an epoch, so provider prompt caches and reasoning state survive.
- Archive and retrieve context without destroying audit history.
- Explain every inclusion and exclusion decision.
- Survive restart and concurrent ingestion.
- Replay recorded histories deterministically and evaluate managed against full-context execution.
- Provide fake, OpenAI-compatible, and Anthropic-compatible provider adapters.

### V1 non-goals

Temporal, Vicegerent, Kubernetes, distributed context service, Rust, vector databases, custom tokenizers, UI, vMCP, and a general operating system are deferred. Also out of scope for V1:

- Cross-session memory. No item is visible outside its session (INV-05).
- Runtime-generated summaries. The runtime stores and uses summary items supplied by the harness or agent with provenance (FR-REL-002); core never calls a model.
- LLM-based classification, relationship detection, or obligation matching.
- Content redaction and deletion. Stores are treated as secret-bearing (section 9).

Interfaces may leave room for them, but V1 must prove runtime semantics first.

## 3. Functional requirements

Requirement IDs are stable and are part of the test contract. New requirements take the next free ID in their group; removed IDs are not reused.

### Ingestion and authority

- FR-ING-001: Every event enters one pipeline, in this order: normalize; assign authority per span; parse directives in authorized spans; classify (a directive's kind wins over heuristics); apply generation, scope, and retention defaults; deduplicate; detect relationships; count tokens with each registered counter; assign a sequence number; persist in one transaction.
- FR-ING-002: Authorities are SYSTEM, HARNESS, USER, AGENT, TOOL, and RETRIEVED_CONTENT. Precedence is SYSTEM > HARNESS > USER > AGENT > TOOL = RETRIEVED_CONTENT. TOOL and RETRIEVED_CONTENT cannot override each other. Where a total order is required (FR-ASM-006), TOOL sorts before RETRIEVED_CONTENT.
- FR-ING-003: An event may not raise its own authority, generation, pinned state, scope, retention, or mandatory status. Only an authorized source or runtime policy may grant those properties. Span authority cannot exceed the principal's authority.
- FR-ING-004: Directive parsing applies only to spans with SYSTEM, HARNESS, or USER authority. An event may carry several spans; the harness marks embedded content it knows about (attachments, file contents, echoed tool output) as TOOL or RETRIEVED_CONTENT spans. Directive headings inside fenced code blocks or block quotes are never parsed. Tool output and retrieved content remain content even when they contain directive-looking headings.
- FR-ING-005: Stable source identity, content hash, or explicit relationship data must make duplicate events detectable while preserving audit references. Duplicates are linked with DUPLICATE_OF, never merged. Deduplication never crosses session, scope, or authority boundaries, and a duplicate never inherits the canonical item's authority, generation, retention, or mandatory status.

### Directives

- FR-DIR-001: V1 recognizes the content directives Goal, Pinned, Working, Remember, References, and Ephemeral, and the lifecycle commands Resolve and Unpin.
- FR-DIR-002: Directive IDs use [id] syntax and are stored independently from display text. IDs are unique within task scope. A content directive without an id receives a derived ID (the lowercased keyword, a hyphen, and the first 8 hex digits of its content hash), reported in diagnostics and inspection. Reusing an ID in the same task creates a new item that SUPERSEDES the previous one, subject to FR-REL-006.
- FR-DIR-003: Defaults are:
  - Goal: goal kind, durable generation, task scope, PROTECTED retention and mandatory while active.
  - Pinned: constraint kind (instruction via kind=), pinned generation, task scope, PROTECTED retention, mandatory.
  - Working: task_state kind (conversation via kind=), working generation, task scope, NORMAL retention.
  - Remember: fact kind (decision or summary via kind=), durable generation, task scope, HIGH retention.
  - References: reference kind, working generation, task scope, NORMAL retention. Each body line that names an existing item ID in scope gets a REFERENCES edge.
  - Ephemeral: evidence kind (tool_result via kind=), ephemeral generation, TURN scope, LOW retention.
- FR-DIR-004: Parser output includes source spans, directive ID, source authority, parsed scope, parsed TTL, and diagnostics. Malformed directives do not discard unrelated content.
- FR-DIR-005: Resolve [id] ends an active goal: its lifecycle becomes RESOLVED, it stops being mandatory, and its retention drops to HIGH. Unpin [id] moves a pinned item to the durable generation with HIGH retention. Both require authority at least equal to the target's, are recorded as lifecycle events, and produce a diagnostic if the ID is unknown in the task. Other lifecycle words produce ErrUnsupportedDirective diagnostics rather than ambiguous mutations.
- FR-DIR-006: Directives follow this grammar (ABNF):

      directive  = marker SP keyword [SP "[" id "]"] *(SP attr) EOL body
      marker     = 1*6"#"
      keyword    = "Goal" / "Pinned" / "Working" / "Remember" / "References"
                 / "Ephemeral" / "Resolve" / "Unpin"
      id         = 1*64(ALPHA / DIGIT / "-" / "_" / ".")
      attr       = attr-name "=" value
      attr-name  = "kind" / "scope" / "ttl" / "obligation"
      value      = 1*(ALPHA / DIGIT / "-" / "_" / ".")
      body       = all lines until the next heading of the same or higher level, or the end of the span

  Keywords match case-insensitively; everything else is exact. A heading whose first word is not a keyword is ordinary content. Attributes are allowed as follows: kind on Pinned, Working, Remember, and Ephemeral; scope on all content directives; ttl (a positive count of turns) on Working, Remember, References, and Ephemeral; obligation on Pinned. scope= may widen scope beyond TASK only in SYSTEM or HARNESS spans. Resolve and Unpin require an id and have an empty body. Unknown attributes, invalid values, and disallowed attributes produce diagnostics and are ignored.

  Example:

      ## Goal [migrate-http]
      Upgrade the HTTP client to v3 without changing public APIs.

      ## Pinned [tests-green] obligation=tests_pass
      The full test suite must pass before the task is complete.

      ## Remember [retry-choice] kind=decision
      Keep the existing retry policy; v3's default retries are disabled.

      ## Ephemeral ttl=2
      (pasted build output)

      ## Unpin [tests-green]

  Phase 2 adds a canonical example set under testdata/directives/ that is part of the test contract.

### Domain model

- FR-DOM-001: Every item has an immutable ID; session, workflow, task, and agent IDs; kind, generation, authority, scope, lifecycle state, retention class, content parts, content hash, per-counter token counts, sequence number, audit timestamps, usage counters, source reference, and optional event and directive IDs.
- FR-DOM-002: Kinds include goal, constraint, instruction, user_message, assistant_message, conversation, fact, decision, task_state, tool_call, tool_result, error, artifact, reference, summary, evidence, and reasoning. A reasoning item is an opaque provider reasoning block, bound to the provider, model, and epoch that produced it.
- FR-DOM-003: Scopes are TURN, TASK, WORKFLOW, SESSION, and AGENT, with TURN ⊂ TASK ⊂ WORKFLOW ⊂ SESSION. Visibility requires matching session IDs plus: TURN, same task and current turn; TASK, same task; WORKFLOW, same workflow; SESSION, any principal in the session; AGENT, same agent. This matrix is the V1 default; ADR 6 confirms it before Phase 1 exits.
- FR-DOM-004: Generations are PINNED, DURABLE, WORKING, and EPHEMERAL. An item is pinned exactly when its generation is PINNED; there is no separate flag. Promotion and demotion are explicit, audited transitions.
- FR-DOM-005: Lifecycle state is ACTIVE, RESOLVED (goals only), or ARCHIVED, independent of generation. A durable item may be archived and a working item may remain active. Superseded and rehydrated are derived from relationships and retrieval events, not stored.
- FR-DOM-006: Evidence, knowledge, and state are separate semantic categories. Derived knowledge points to evidence; state changes point to the observations or decisions that caused them. Kinds map to categories:
  - Directive: goal, constraint, instruction.
  - Knowledge: fact, decision, summary, reference.
  - Evidence: tool_result, error, evidence, artifact. The evidence kind covers evidence not produced by a tool, such as a harness assertion or a user-supplied log.
  - State: task_state.
  - Conversation: user_message, assistant_message, conversation, tool_call, reasoning.
- FR-DOM-007: Mandatory status is derived at assembly time, never stored. An item is mandatory when it is system policy (a SYSTEM-authority instruction), a security constraint (a pinned constraint with SYSTEM or HARNESS authority), part of the pending input or its exchange group, an active goal, pinned, or an UNRESOLVED or BLOCKED obligation (unless FR-OBL-003 policy disables it).
- FR-DOM-008: Content, kind, authority, IDs, scope, source, and sequence number are immutable. Generation, lifecycle state, retention class, usage counters, and version change only through audited lifecycle events.

### Provenance, supersession, and obligations

- FR-REL-001: Relationships support DERIVED_FROM, SUPERSEDES, DEPENDS_ON, REFERENCES, SATISFIES, and DUPLICATE_OF. Relationship records are the only store of edges; items do not duplicate them.
- FR-REL-002: A derived item cannot be persisted with missing required provenance. Dangling relationship IDs are rejected with ErrDanglingRelationship.
- FR-REL-003: Supersession removes obsolete state from active assembly at the next rebase while retaining both nodes and the edge for audit and replay.
- FR-REL-004: Supersession is acyclic. A cycle is a transaction error.
- FR-REL-005: A “why do we believe this?” query traverses provenance to evidence under the same authorization and scope checks. Nodes the principal cannot see are omitted and the path is marked truncated.
- FR-REL-006: A SUPERSEDES edge requires the superseding item's authority to be at least the superseded item's. A violating edge fails with ErrInvalidAuthorityPromotion.
- FR-REL-007: V1 creates relationships only from explicit relationship data on the event, directive ID reuse (FR-DIR-002), and deterministic policy rules. The V1 rule set: a tool_result DEPENDS_ON the tool_call it answers, and a tool_result from the same tool with the same canonicalized arguments in the same task supersedes the earlier result. Rules are versioned with the policy.
- FR-OBL-001: An obligation has a stable ID, description, status, scope, owner/source, creation time, matcher, and satisfying evidence IDs. In V1, obligations are created explicitly: by a Pinned directive with obligation=<matcher>, or by a HARNESS event that declares one. The runtime never infers obligations from free text.
- FR-OBL-002: Statuses are UNRESOLVED, SATISFIED, BLOCKED, and WAIVED. Status history is append-only. Allowed transitions:
  - UNRESOLVED to SATISFIED: the obligation's matcher accepts evidence, or a HARNESS assertion. Satisfying evidence IDs are recorded.
  - SATISFIED to UNRESOLVED: automatic when satisfying evidence is superseded by evidence the matcher rejects, or by HARNESS assertion.
  - UNRESOLVED to BLOCKED and back: HARNESS or USER.
  - Any status to WAIVED: a principal with authority at least the obligation's source authority. WAIVED is terminal.

  AGENT, TOOL, and RETRIEVED_CONTENT cannot change obligation status. Other transitions fail with ErrInvalidTransition.
- FR-OBL-003: Unresolved and blocked obligations are mandatory task state unless authorized policy disables obligation materialization.
- FR-OBL-004: Tool evidence satisfies an obligation only through its deterministic matcher or an authorized harness assertion. Matchers are registered by name and versioned with the policy.

### Assembly

- FR-ASM-001: Assemble(principal, request) takes the pending input item IDs, provider and model, token budget, reserved output tokens, and the harness's tool definitions. It returns normalized messages, selected item IDs, assembly ID, epoch ID, mode (REBASE or APPEND), token accounting, and complete decisions. Pending input must already be ingested.
- FR-ASM-002: Assembly partitions candidates into mandatory items (FR-DOM-007) and non-mandatory items. All mandatory items are included or assembly fails (FR-ASM-004). Non-mandatory items are ranked by score (FR-ASM-005); generation contributes to the score but is not a hard tier. Policy v1 weights generation so that, other factors equal, active task state ranks above durable knowledge, then working memory, rehydrated items, and ephemeral evidence. Selection order never determines render order (FR-RND-001).
- FR-ASM-003: The usable budget is min(token budget, model context window) minus reserved output tokens, reasoning headroom, and the counter's safety margin. The counter registered for the target (provider, model) counts every serialized input, including system content, tool definitions, message framing, and pending input. Assembled input tokens, as counted, never exceed the usable budget. An estimating counter's safety margin must cover its declared error bound.
- FR-ASM-004: Mandatory items are never silently discarded or truncated. If they exceed budget, return ErrMandatoryContextExceedsBudget with required/available counts and item IDs, so the caller can Unpin, Resolve, or split the task.
- FR-ASM-005: Non-mandatory selection uses an explainable score defined by the versioned policy. Factors: explicit importance, authority, generation, relevance, dependency value, recurrence, recency, state value, token cost, rehydration cost, duplication, and supersession. V1 relevance is lexical overlap with the pending input and active goals, using the normalization from ADR 7. Recency uses sequence numbers and call indexes, never wall-clock time. Scores are fixed-point integers.
- FR-ASM-006: Ties break by score (descending), authority (descending, FR-ING-002 order), last-used call index (descending), sequence number (ascending), then item ID (ascending).
- FR-ASM-007: Every candidate decision contains included/excluded, a reason code from a closed set, total score and per-factor contributions, generation, authority, token count, rehydrated, superseded, and mandatory fields.
- FR-ASM-008: Session, task, agent, and scope filters run before scoring. Cross-session items never enter the candidate set.
- FR-ASM-009: Packing works on exchange groups: a group is included or excluded as a unit. An included item's required DEPENDS_ON targets are included too, or the item is excluded.
- FR-ASM-010: Assembly runs in epochs. In APPEND mode, the rendered request is the previous request in the epoch, unchanged, plus the items ingested since; decisions are recorded for the new items only. Items already rendered stay rendered; changes to the candidate set (supersession, GC, TTL and TURN-scope expiry) take effect at the next rebase. The runtime rebases when the task has no epoch; the provider, model, or tool definitions change; a SYSTEM or HARNESS item arrives and the adapter has no append-only form for it; appending would exceed the usable budget; or policy requests it, for example after GC under pressure.
- FR-ASM-011: Reasoning items are replayed verbatim within their epoch and never after a rebase. A rebase should happen only when no tool round is open. If the budget forces one while a round is open, the open round's reasoning is dropped and a reasoning_dropped event is recorded.

### Rendering

- FR-RND-001: Render order is fixed and independent of selection order: (1) system content: system policy, security constraints, active goals, pinned requirements, and unresolved obligations, in sequence order; (2) a context section with the selected non-mandatory items grouped by category, in sequence order; (3) the conversation: selected exchange groups in chronological order, ending with the pending input. Items added during an epoch are appended where they occur and move to their section at the next rebase.
- FR-RND-002: Authority maps to provider roles without escalation. SYSTEM and HARNESS render as system content, or as provider mid-conversation system messages where supported; USER as user; AGENT as assistant; TOOL as tool results; RETRIEVED_CONTENT as delimited data inside user or tool-result content. TOOL, RETRIEVED_CONTENT, and embedded spans render inside adapter-defined delimiters labeled with their authority. Occurrences of the runtime's delimiters or section headers inside untrusted content are escaped.
- FR-RND-003: Every rendered request satisfies the target provider's structural rules, including message ordering, tool call and result pairing, and reasoning placement. Assembly fails with ErrInvalidProviderRequest rather than return an invalid request.
- FR-RND-004: At a rebase, excluded or archived items that policy judges relevant may render as stubs (ID, kind, short description, token size) within a policy stub budget, so the model can request them (FR-RET-006).

### Garbage collection, retrieval, and persistence

- FR-GC-001: GC runs on pressure (task active tokens above a policy threshold, expressed as a fraction of the usable budget), task completion, supersession, TTL expiration, explicit lifecycle command, or policy request.
- FR-GC-002: GC archives by default. Automatic deletion is outside V1.
- FR-GC-003: Pinned items and active goals survive every automatic GC path, including TTL expiration. Superseded state may leave active assembly but remains auditable.
- FR-GC-004: Stale, duplicated, superseded, and unused ephemeral items are preferentially collectible.
- FR-GC-005: GC is transactional and idempotent.
- FR-GC-006: GC changes to the active set reach the rendered request at the next rebase. GC under pressure requests a rebase.
- FR-RET-001: The archive supports Search, Get, and Rehydrate. Each call carries a principal and applies the same scope checks as assembly.
- FR-RET-002: V1 search works without a vector database through a deterministic lexical/indexed implementation.
- FR-RET-003: Rehydration records the principal, triggering actor, query, item IDs, latency, success/failure, and assembly usage.
- FR-RET-004: Repeated successful rehydration increases retention priority through an explainable recurrence signal. Rehydrations of one item within an epoch count once, and the signal has a policy cap, so the model cannot raise an item's retention without limit.
- FR-RET-005: A pruning-regret event occurs when an item excluded or archived at a rebase is rehydrated before the next rebase in the same task. Regret metrics report event count, rehydrated tokens, model calls between exclusion and rehydration, and failed or slow rehydrations.
- FR-RET-006: The runtime provides provider-neutral tool definitions and handlers for archive search and rehydration that the harness may register with the model. The rehydrate handler returns the item's content as the tool result, so it enters the conversation without a rebase. Rehydrated items become ACTIVE and are protected from automatic GC for a policy-defined number of model calls.
- FR-PER-001: V1 provides in-memory and SQLite stores; Postgres may implement the same interface after SQLite acceptance.
- FR-PER-002: Persistence covers items, relationships, generations, authority, scope, lifecycle state, obligations and their transitions, lifecycle events, epochs, assembly records, retrieval events, token counts, and provider usage.
- FR-PER-003: Restart reconstructs the same logical state and relationships.
- FR-PER-004: Mutations use transaction protection and preserve graph integrity under concurrent ingestion.

### Providers and observability

- FR-PROV-001: Core uses a normalized provider interface for messages, responses, and usage metadata.
- FR-PROV-002: V1 includes deterministic fake, OpenAI-compatible, and Anthropic-compatible adapters. Adapters are stateless: they send the full rendered request and never rely on provider-side conversation state. ADR 9 fixes the OpenAI API surface.
- FR-PROV-003: Adapters cannot apply lifecycle policy or change assembly decisions. Provider-native context management (server-side compaction, context editing) is disabled by default; enabling it is a policy decision recorded on each assembly.
- FR-PROV-004: Each adapter provides a serializer and a token counter for its models. They implement interfaces defined in internal/domain, so assembly does not import adapters.
- FR-PROV-005: Adapters return provider reasoning blocks as reasoning items for ingestion; replay follows FR-ASM-011.
- FR-PROV-006: Adapters report provider usage, including cache-read and cache-write input tokens where the provider reports them.
- FR-OBS-001: Emit structured events for ingestion, parsing, relationships, transitions, GC, retrieval, rehydration, epochs and rebases, assembly, provider calls, obligations, and errors.
- FR-OBS-002: Report active tokens, archived tokens, assembled tokens, reduction, cache-read and cache-write tokens, estimated cost, rebases, GC counts, rehydrations, reasoning drops, latency, regret, provider latency, counter error (provider-reported minus counted input tokens), and errors.
- FR-OBS-003: Every assembly is replayable from the ordered event log (ingestion, lifecycle, retrieval, and assembly side effects), policy version, counter identity, model, and budget.
- FR-OBS-004: Identical inputs, policy, and counters produce identical candidate ordering, decisions, and rendered output.
- FR-OBS-005: Offline replay runs a recorded history through the managed assembler with the fake provider and reports tokens, peak context, rebases, and invariant violations. Live evaluation runs the benchmark against a real model for each arm and compares correctness, tokens, cache usage, cost, peak context, calls, rehydrations, regret, latency, and invariant violations (section 12).

## 4. Non-functional requirements

Values are proposed and are confirmed at Phase 9.

- NFR-001: Assemble p95 latency is at most 100 ms with 10,000 candidate items in one task on the SQLite store, excluding remote token counting.
- NFR-002: Ingest p95 latency is at most 20 ms per event, excluding remote token counting.
- NFR-003: Remote counting is called at most once per item per counter (counts are cached by content hash), plus at most one whole-request count per assembly.
- NFR-004: The benchmark reports store growth per model call, including assembly records and decisions.

## 5. Hard invariants

- INV-01: Assembled input tokens, as counted by the target's registered counter, never exceed the usable token budget.
- INV-02: Mandatory context never disappears silently.
- INV-03: Pinned directives and active goals survive automatic GC.
- INV-04: Lower-authority content cannot promote itself or override higher-authority context, including through supersession, deduplication, or obligation transitions.
- INV-05: Cross-session items, and items outside the principal's task, workflow, or agent visibility, never enter assembly, retrieval, explanation, or inspection results.
- INV-06: Supersession graphs are acyclic.
- INV-07: Required derived items retain valid provenance.
- INV-08: Archived items remain auditable and rehydratable.
- INV-09: Given caller-supplied stable event IDs, identical inputs produce deterministic assembly decisions and rendered output.
- INV-10: Restart and concurrent mutation preserve valid state and relationships.
- INV-11: Every rendered request is structurally valid for its target provider.
- INV-12: Within an epoch, each rendered request extends the previous one without changing it.

## 6. Proposed Go package boundaries

Use Go initially because the plan’s domain examples and concurrency requirements align with it. The public API lives outside internal/ so harnesses in other modules can import it. Keep dependency direction one way:

    .                     package contextruntime: public Runtime interface, request and response types, errors
    internal/domain       pure types, enums, transitions, validation, serializer and counter interfaces
    internal/directive    source-gated markdown parser and diagnostics
    internal/store        store interfaces, memory store, SQLite store
    internal/graph        provenance and supersession operations
    internal/policy       scoring, retention, authority, lifecycle policy, relationship rules
    internal/ingest       normalization, classification, relationship detection
    internal/obligation   matchers, creation, and satisfaction
    internal/assemble     filtering, epochs, packing, render sections, decision records
    internal/retrieve     archive search, rehydration, retrieval tool handlers
    internal/provider     adapters: serializers, counters, transports
    internal/telemetry    events, metrics, explanations
    internal/replay       history runner and baseline comparison
    cmd/context-runtime   optional executable and inspection commands

internal/domain imports no other internal package. internal/provider and internal/telemetry import only internal/domain. The root package re-exports domain types with type aliases. Policy functions should be pure where possible for property testing and replay.

## 7. Normative data model

The exact representation may evolve, but these semantics are required:

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

Mandatory, superseded, and rehydrated status are derived (FR-DOM-005, FR-DOM-007). Edges live only in Relationship records (FR-REL-001).

Required companion records are Relationship, Obligation, ObligationTransition, LifecycleEvent, Epoch, AssemblyRecord, AssemblyDecision, RetrievalEvent, and ProviderUsage.

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

The embedding process constructs principals and is trusted to do so. Lifecycle commands (Resolve, Unpin) arrive as directives through Ingest. CompleteTask resolves the task's active goals and runs GC for task completion.

Machine-checkable errors include ErrMandatoryContextExceedsBudget, ErrNotFound, ErrInvalidAuthorityPromotion, ErrMissingProvenance, ErrDanglingRelationship, ErrSupersessionCycle, ErrInvalidTransition, ErrInvalidProviderRequest, and ErrVersionConflict. ErrUnsupportedDirective and ErrMalformedDirective are diagnostic codes, not returned errors. Cross-session and unauthorized lookups return ErrNotFound so callers cannot probe for existence; the runtime records them as ErrCrossSessionAccess security events.

## 9. Security specification

- Trust is metadata assigned at ingestion, never inferred from content claims. Span authority cannot exceed the principal's authority.
- Directive parsing is source gated (FR-ING-004). A retrieved document containing a Pinned heading remains retrieved content.
- Tool output cannot pin itself, promote itself, supersede or deduplicate into higher-authority items, change obligation status, or override higher-authority context.
- Retrieval, explanation, provenance, and inspection APIs enforce the same session, task, agent, and scope checks as assembly.
- The runtime guarantees its own metadata and decisions. It cannot stop a model from following instructions inside untrusted content; it reduces that risk by rendering untrusted content as delimited, labeled data (FR-RND-002).
- Stores hold full tool output and may contain secrets. V1 creates SQLite files readable only by their owner, and telemetry events carry IDs and hashes rather than content unless a debug policy enables content. Redaction is deferred (section 2).
- Tests cover retrieved injection, tool injection, delimiter spoofing, authority downgrade, supersession and deduplication across authority, cross-session lookup, cross-task scope, and provenance access controls. The directive parser is fuzzed.

## 10. Determinism and concurrency

Determinism is required for parsing diagnostics, item IDs when the caller supplies a stable event ID, relationship ordering, scoring, packing, rendering, GC decisions, and assembly records. Time and random IDs are injectable in tests.

- Scoring and recency use sequence numbers and call indexes, not wall-clock time.
- Assembly side effects (usage counters, last-used call index, epoch state) are recorded as lifecycle events, so replay reapplies them in order.
- Scores are fixed-point integers. Go permits fusing floating-point multiply-add, and the gc compiler does so on arm64 but not on default amd64 builds, so floating-point scores could differ between an Apple Silicon laptop and an amd64 CI runner.
- Sequence numbers are assigned per session at commit and define the order replay uses.

Mutations serialize per session, because items at WORKFLOW, SESSION, and AGENT scope are shared across tasks; transaction retries are allowed on conflict. A successful operation is linearizable from the session's perspective. Tests run with the race detector and cover concurrent duplicate ingestion, supersession, GC, and restart snapshots.

## 11. Delivery phases and exit gates

Each phase lists the ADRs (section 15) that must be accepted before it exits.

1. Domain and stores: enums, validation, scopes, authority, generations, lifecycle, relationships, sequence numbers, memory store, SQLite schema, and transactions. ADRs 1, 3, 4, 6, 13. Gate: unit, persistence, restart, graph integrity, and race tests.
2. Directives and ingestion: grammar, parser, IDs, defaults, diagnostics, normalization, spans, classification, deduplication, and relationship rules. Gate: canonical directive examples, parser fuzzing, and injection tests.
3. Assembler: candidate filtering, mandatory partition, scoring, exchange-group packing, epochs, render sections, counter and serializer interfaces, accounting, and explanations. Uses the fake adapter, which enforces ordering and tool-pairing rules at least as strict as the real providers. ADRs 2, 5, 7, 11. Gate: budget, validity, and prefix-stability property tests; overflow error; deterministic records.
4. GC and archive: lifecycle transitions, TTL, stale scoring, deduplication, and supersession-aware collection. Gate: pinned/goal protection and auditability.
5. Retrieval and rehydration: lexical search, retrieval tools, stubs, access tracking, retention feedback, and regret. Gate: archive recovery and explainable priority change.
6. Obligations: explicit creation, matchers, transitions, evidence links, and status history. ADR 8. Gate: unresolved visibility, proof-backed satisfaction, and reversion when evidence is superseded.
7. Providers: fake, OpenAI-compatible, and Anthropic-compatible adapters, including reasoning handling and cache usage reporting. ADRs 9, 12. Gate: adapter contract and golden serialization tests preserve normalized semantics and structural validity.
8. Replay and workload: offline replay, live runner, baselines, deterministic coding task, and large noisy outputs. ADR 10. Gate: all arms complete and emit metrics.
9. Evaluation and hardening: all test classes, security, profiling, and benchmark review. ADR 14. Gate: hard invariants pass, NFR targets are met, and target results are reported.

## 12. Test specification

### Unit tests

Cover directive grammar, defaults, and diagnostics; authority ordering; generation and lifecycle transitions; kind classification; relationship validation and rules; provenance traversal; supersession cycles; obligation transitions; score calculation; stable ties; budget overflow; token accounting; epoch and rebase triggers; rendering and escaping; search; rehydration; and regret metrics.

### Property tests

Prove that assembly never exceeds usable budget, every rendered request is structurally valid for its provider, each request within an epoch extends the previous one unchanged, GC never archives a pinned item or active goal automatically, no assembly contains another session’s items, supersession remains acyclic, derived items have valid provenance, identical seeded inputs assemble identically, and valid event replay reconstructs the same state.

### Fuzz tests

Fuzz the directive parser and the renderer's escaping with Go native fuzzing.

### Integration tests

Prove SQLite restart recovery, schema migrations, concurrent ingestion integrity, authority preservation through storage and assembly, fake-provider end-to-end flow, archive/rehydration, obligations, and provider adapter contracts with golden serialized requests. Run all concurrency tests under the race detector.

### End-to-end benchmark

Use a deterministic dependency migration task with repository reads, architecture docs, dependency metadata, large build failures, source edits, test failures, changelog inspection, correction, passing tests, and final response.

Offline replay runs recorded histories through the managed assembler with the fake provider. Live evaluation runs a real model in three arms:

- BASELINE-FULL: the full transcript each call. When it exceeds the context window, the oldest exchange groups are dropped and each drop is recorded.
- BASELINE-PROVIDER: the full transcript with the provider's native context management, where the provider offers it.
- MANAGED: Context Runtime assembly each call.

Each arm runs enough times to report a correctness pass rate with a confidence interval; ADR 10 sets the run count and the correctness oracle. Record correctness, input tokens, cache-read and cache-write tokens, cost per completed task, peak active context, reduction, tool calls, model calls, rebases, rehydrations, pruning regret, latency, and invariant violations.

Reduction is mean assembled input tokens per model call relative to BASELINE-FULL. Initial target: at least 50% reduction with no correctness degradation. Cost per completed task is reported alongside it, so a reduction that raises cost is visible. Hard targets: zero lost pinned requirements, zero lost goals, zero budget violations (checked against provider-reported input tokens), zero invalid provider requests, zero cross-session leakage, and zero provenance corruption.

## 13. Inspection contract

Inspect exposes a text or structured view, similar to a /context command in coding harnesses, with the active goal, pinned items and obligation statuses, working items, durable items, archived counts, token usage, the current epoch, recent GC decisions, and unresolved requirements. Inspection is read-only and uses runtime authorization and scope checks.

## 14. Acceptance checklist

V1 is accepted only when:

- Authorized directives are parsed per the grammar and persisted with source metadata.
- Goals and pinned requirements survive every automatic GC path, and Resolve and Unpin release them.
- Tool and retrieved content cannot create privileged directives or override higher-authority items through supersession, deduplication, or obligations.
- Evidence, knowledge, state, provenance, and supersession are separate and queryable through Get, Search, and Provenance.
- Unresolved obligations are visible, evidence-backed satisfaction works, and superseded evidence reverts satisfaction.
- Archived evidence is searchable and rehydratable by the harness and through the retrieval tools.
- Rehydration and pruning regret are measurable.
- Every assembly has deterministic inclusion/exclusion explanations.
- Mandatory context never silently disappears and token budget is never exceeded.
- Every rendered request is valid for its provider, and requests within an epoch are append-only.
- Session and task isolation is enforced.
- Restart and concurrent operations preserve valid state.
- All provider adapters pass contract tests.
- Offline replay and live evaluation compare the baselines with managed execution.
- The benchmark meets hard invariants and NFR targets, and reports reduction, cost, and cache usage.

## 15. ADRs required before affected phase exits

Section 11 maps each ADR to the phase it gates.

1. Go module path and minimum Go version.
2. Token counter strategy per provider and model: exact or estimating counters, error bounds, safety margins, and use of provider count endpoints.
3. SQLite driver and migration mechanism.
4. Stable ID format and event ID requirements.
5. Scoring policy v1: factor weights, fixed-point scale, GC pressure threshold, and stub budget. (Replaces "Whether Pinned always implies Mandatory", which FR-DIR-003 and FR-DOM-007 now decide.)
6. Scope containment matrix (confirms FR-DOM-003).
7. Lexical index and normalization rules.
8. Obligation matchers and false-positive handling.
9. Provider transport libraries, retry policy, and the OpenAI API surface.
10. Benchmark fixture, correctness oracle, run count, and cost target.
11. Render templates and delimiters per provider.
12. Provider reasoning handling, including rebases during an open tool round, and provider-native context management.
13. Deployment model: embedded library, sidecar process, or both.
14. Content redaction and retention after V1.

Each ADR records the decision, alternatives, compatibility impact, and tests that lock the behavior.

## Appendix A: Development tooling (non-normative)

Use the current flagship reasoning model, GPT-6 Astra, with high reasoning effort for architecture, implementation, debugging, and long-horizon coding. Use extra-high reasoning selectively for security review, replay correctness, and final hardening when evaluation shows a measurable benefit. Use the balanced or efficient model tiers for routine mechanical work after the contract and tests exist.

This workload needs judgment across a large design surface, tool use, deterministic implementation, and failure analysis. Current official OpenAI guidance recommends Astra for ambitious deliverables and complex reasoning, recommends high reasoning for complex agentic workflows, and recommends extra-high for long-running agentic tasks. This guidance will age and does not constrain the runtime, which remains provider independent.

Sources: [OpenAI model selection](https://developers.openai.com/api/docs/guides/model-selection), [OpenAI reasoning models](https://developers.openai.com/api/docs/guides/reasoning).
