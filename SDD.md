# Context Runtime: Spec Driven Development

Status: proposed  
Version: 1.0  
Date: 2026-09-25

## 1. Purpose

Context Runtime is a model independent runtime between an agent harness and an LLM provider. It constructs a bounded working-memory view from the complete transcript, harness state, tool activity, and retrieved content.

The transcript is the immutable audit history. Runtime state determines what is active, archived, rehydrated, superseded, or unresolved for each model invocation.

This specification is the V1 implementation contract: behavior, interfaces, invariants, persistence, testing, and delivery gates.

## 2. Model selection

Use the current flagship reasoning model, GPT-6 Astra, with high reasoning effort for architecture, implementation, debugging, and long-horizon coding. Use extra-high reasoning selectively for security review, replay correctness, and final hardening when evaluation shows a measurable benefit. Use the balanced or efficient model tiers for routine mechanical work after the contract and tests exist.

This workload needs judgment across a large design surface, tool use, deterministic implementation, and failure analysis. Current official OpenAI guidance recommends Astra for ambitious deliverables and complex reasoning, and identifies medium/high reasoning as appropriate for agentic coding and long-horizon work. The runtime remains provider independent; this recommendation applies to development and benchmark agents.

Sources: [OpenAI model selection](https://developers.openai.com/api/docs/guides/model-selection), [OpenAI reasoning models](https://developers.openai.com/api/docs/guides/reasoning).

## 3. Goals and non-goals

### Goals

- Parse authorized context directives from user and harness input.
- Preserve source authority and prevent lower authority from promoting itself.
- Track goals, constraints, knowledge, evidence, state, obligations, provenance, and supersession.
- Assemble deterministic context within a strict usable token budget.
- Archive and retrieve context without destroying audit history.
- Explain every inclusion and exclusion decision.
- Survive restart and concurrent ingestion.
- Replay identical histories and compare managed and full-context execution.
- Provide fake, OpenAI-compatible, and Anthropic-compatible provider adapters.

### V1 non-goals

Temporal, Vicegerent, Kubernetes, distributed context service, Rust, vector databases, custom tokenizers, UI, vMCP, and a general operating system are deferred. Interfaces may leave room for them, but V1 must prove runtime semantics first.

## 4. Functional requirements

Requirement IDs are stable and are part of the test contract.

### Ingestion and authority

- FR-ING-001: Every event enters one pipeline: normalize, assign authority, classify, apply directives, detect relationships, assign generation, and persist.
- FR-ING-002: Authorities are SYSTEM, HARNESS, USER, AGENT, TOOL, and RETRIEVED_CONTENT. Precedence is SYSTEM > HARNESS > USER > AGENT > TOOL / RETRIEVED_CONTENT.
- FR-ING-003: An event may not raise its own authority, generation, pinned state, scope, or mandatory status. Only an authorized source or runtime policy may grant those properties.
- FR-ING-004: Directive parsing is enabled only for authorized system, harness, and user prompt sections. Tool output and retrieved content remain content even when they contain directive-looking headings.
- FR-ING-005: Stable source identity, content hash, or explicit relationship data must make duplicate events detectable while preserving audit references.

### Directives

- FR-DIR-001: V1 recognizes Goal, Pinned, Working, Remember, References, and Ephemeral.
- FR-DIR-002: Directive IDs use [id] syntax and are stored independently from display text. IDs are stable within task scope.
- FR-DIR-003: Defaults are:
  - Goal: goal kind, durable generation, task scope, high retention, active mandatory.
  - Pinned: constraint or instruction kind, pinned generation, task scope, mandatory.
  - Working: task_state or conversation kind, working generation, task scope, collectible.
  - Remember: fact, decision, or summary kind, durable generation, task scope, high retention.
  - References: reference kind, working generation, task scope, retrieval dependency.
  - Ephemeral: evidence or tool_result kind, ephemeral generation, turn/task scope, aggressive collection.
- FR-DIR-004: Parser output includes source spans, directive ID, source authority, parsed scope, parsed TTL, and diagnostics. Malformed directives do not discard unrelated content.
- FR-DIR-005: The model stores lifecycle concepts such as Resolve and Unpin from the beginning. Unsupported commands produce diagnostics rather than ambiguous mutations.

### Domain model

- FR-DOM-001: Every item has immutable ID, session ID, task ID, kind, generation, authority, scope, content, token count, timestamps, retention metadata, source reference, and relationship fields.
- FR-DOM-002: Kinds include goal, constraint, instruction, user_message, conversation, fact, decision, task_state, tool_call, tool_result, error, artifact, reference, summary, and evidence.
- FR-DOM-003: Scopes are TURN, TASK, WORKFLOW, SESSION, and AGENT. Visibility requires scope containment plus session and agent isolation.
- FR-DOM-004: Generations are PINNED, DURABLE, WORKING, and EPHEMERAL. Promotion and demotion are explicit, audited transitions.
- FR-DOM-005: Archived is lifecycle state independent of generation. A durable item may be archived and a working item may remain active.
- FR-DOM-006: Evidence, knowledge, and state are separate semantic categories. Derived knowledge points to evidence; state changes point to the observations or decisions that caused them.

### Provenance, supersession, and obligations

- FR-REL-001: Relationships support DERIVED_FROM, SUPERSEDES, DEPENDS_ON, REFERENCES, SATISFIES, and DUPLICATE_OF.
- FR-REL-002: A derived item cannot be persisted with missing required provenance. Dangling relationship IDs are rejected.
- FR-REL-003: Supersession removes obsolete state from normal active assembly while retaining both nodes and the edge for audit and replay.
- FR-REL-004: Supersession is acyclic. A cycle is a transaction error.
- FR-REL-005: A “why do we believe this?” query traverses provenance to evidence under the same authorization and scope checks.
- FR-OBL-001: A pinned requirement may create an obligation with stable ID, description, status, scope, owner/source, creation time, and satisfying evidence IDs.
- FR-OBL-002: Statuses are UNRESOLVED, SATISFIED, BLOCKED, and WAIVED. Status history is append-only.
- FR-OBL-003: Unresolved obligations are mandatory task state unless authorized policy disables obligation materialization.
- FR-OBL-004: Tool evidence satisfies an obligation only through a deterministic matcher or authorized harness assertion.

### Assembly

- FR-ASM-001: Assemble(task, currentRequest, model, tokenBudget) returns normalized messages, selected item IDs, assembly ID, token accounting, and complete decisions.
- FR-ASM-002: Ordering priority is system policy, security constraints, current request, active goal, pinned requirements, unresolved obligations, active task state, relevant durable knowledge, working memory, rehydrated information, then relevant ephemeral evidence.
- FR-ASM-003: Every serialized provider input is counted and assembled input tokens <= usable token budget always holds. Output and reasoning headroom is reserved before packing.
- FR-ASM-004: Mandatory items are never silently discarded. If they exceed budget, return ErrMandatoryContextExceedsBudget with required/available counts and item IDs.
- FR-ASM-005: Non-mandatory selection uses an explainable score based initially on explicit importance, authority, relevance, dependency value, recurrence, recency, state value, token cost, rehydration cost, duplication, and supersession.
- FR-ASM-006: Tie breaking is stable: mandatory rank, score, authority, last-used time, then item ID.
- FR-ASM-007: Every candidate decision contains included/excluded, score, generation, authority, token count, reason, rehydrated, superseded, and mandatory fields.
- FR-ASM-008: Session, task, agent, and scope filters run before scoring. Cross-session items never enter the candidate set.

### Garbage collection, retrieval, and persistence

- FR-GC-001: GC runs on pressure, phase completion, supersession, TTL expiration, explicit lifecycle command, or policy request.
- FR-GC-002: GC archives by default. Automatic deletion is outside V1.
- FR-GC-003: Pinned items and active goals survive automatic GC. Superseded state may leave active assembly but remains auditable.
- FR-GC-004: Stale, duplicated, superseded, and unused ephemeral items are preferentially collectible.
- FR-GC-005: GC is transactional and idempotent.
- FR-RET-001: The archive supports Search(query), Get(id), and Rehydrate(id).
- FR-RET-002: V1 search works without a vector database through a deterministic lexical/indexed implementation.
- FR-RET-003: Rehydration records request, query, item IDs, latency, success/failure, and assembly usage.
- FR-RET-004: Repeated successful rehydration increases retention priority through an explainable recurrence signal.
- FR-RET-005: Failed, repeated, and slow rehydrations contribute to pruning-regret metrics.
- FR-PER-001: V1 provides in-memory and SQLite stores; Postgres may implement the same interface after SQLite acceptance.
- FR-PER-002: Persistence covers items, relationships, generations, authority, scope, archive state, obligations, lifecycle events, assembly records, and retrieval metrics.
- FR-PER-003: Restart reconstructs the same logical state and relationships.
- FR-PER-004: Mutations use transaction protection and preserve graph integrity under concurrent ingestion.

### Providers and observability

- FR-PROV-001: Core uses a normalized provider interface for messages, responses, and usage metadata.
- FR-PROV-002: V1 includes deterministic fake, OpenAI-compatible, and Anthropic-compatible adapters.
- FR-PROV-003: Adapters cannot apply lifecycle policy or change assembly decisions.
- FR-OBS-001: Emit structured events for ingestion, parsing, relationships, transitions, GC, retrieval, rehydration, assembly, provider calls, obligations, and errors.
- FR-OBS-002: Report active tokens, archived tokens, assembled tokens, reduction, GC counts, rehydrations, latency, regret, provider latency, and errors.
- FR-OBS-003: Every assembly is replayable from history, policy version, tokenizer version, and model budget.
- FR-OBS-004: Identical inputs, policy, and tokenizer produce identical candidate ordering, decisions, and assembled output.
- FR-OBS-005: Replay compares full transcript and managed context on correctness, tokens, peak context, calls, rehydrations, regret, latency, and invariant violations.

## 5. Hard invariants

1. Assembled input tokens never exceed usable token budget.
2. Mandatory context never disappears silently.
3. Pinned directives and active goals survive automatic GC.
4. Lower-authority content cannot promote itself or override higher-authority context.
5. Cross-session and unauthorized cross-scope items never enter assembly or retrieval results.
6. Supersession graphs are acyclic.
7. Required derived items retain valid provenance.
8. Archived items remain auditable and rehydratable.
9. Identical inputs produce deterministic assembly decisions.
10. Restart and concurrent mutation preserve valid state and relationships.

## 6. Proposed Go package boundaries

Use Go initially because the plan’s domain examples and concurrency requirements align with it. Keep dependency direction one way:

    internal/domain       pure types, enums, transitions, validation
    internal/directive    source-gated markdown parser and diagnostics
    internal/store        interfaces, memory store, SQLite store
    internal/graph        provenance and supersession operations
    internal/ingest       normalization, classification, relationship detection
    internal/policy       scoring, retention, authority, lifecycle policy
    internal/assemble     filtering, packing, decision records
    internal/retrieve     archive search and rehydration
    internal/obligation   derivation and satisfaction
    internal/provider     normalized interface and adapters
    internal/telemetry    events, metrics, explanations
    internal/replay       history runner and baseline comparison
    cmd/context-runtime   optional executable and inspection commands

The domain package must not import provider, storage, or telemetry. Policy functions should be pure where possible for property testing and replay.

## 7. Normative data model

The exact representation may evolve, but these semantics are required:

    type ContextItem struct {
        ID           string
        SessionID    string
        TaskID       string
        Kind         ContextKind
        Generation   Generation
        Authority    Authority
        Scope        Scope
        Content      string
        Tokens       int
        Importance   float64
        CreatedAt    time.Time
        LastUsedAt   time.Time
        AccessCount  int
        Pinned       bool
        Mandatory    bool
        Rehydratable bool
        Archived     bool
        TTLTurns     *int
        Tags         []string
        Source       *SourceRef
        DerivedFrom  []string
        Supersedes   []string
        DependsOn    []string
        References   []string
        Version      uint64
    }

Required companion records are Relationship, Obligation, LifecycleEvent, AssemblyRecord, AssemblyDecision, RetrievalEvent, and ProviderUsage.

## 8. Public service contract

The first public API may be Go methods; a wire protocol is deferred:

    type Runtime interface {
        Ingest(ctx context.Context, event Event) ([]ContextItem, error)
        Assemble(ctx context.Context, req AssembleRequest) (Assembly, error)
        Search(ctx context.Context, req SearchRequest) ([]SearchHit, error)
        Get(ctx context.Context, id string) (ContextItem, error)
        Rehydrate(ctx context.Context, id string) (ContextItem, error)
        Collect(ctx context.Context, req CollectRequest) (CollectReport, error)
        ExplainAssembly(ctx context.Context, assemblyID string) (AssemblyRecord, error)
    }

Machine-checkable errors include ErrMandatoryContextExceedsBudget, ErrCrossSessionAccess, ErrInvalidAuthorityPromotion, ErrMissingProvenance, ErrSupersessionCycle, ErrVersionConflict, and ErrUnsupportedDirective.

## 9. Security specification

- Trust is metadata assigned at ingestion, never inferred from content claims.
- Directive parsing is source gated. A retrieved document containing a Pinned heading remains retrieved content.
- Tool output cannot pin itself, promote itself, satisfy a high-authority obligation without an authorized matcher, or override higher-authority context.
- Retrieval and explanation APIs enforce the same session, task, agent, and scope checks as assembly.
- Serialized provider content represents untrusted text as data and cannot alter runtime metadata.
- Tests cover retrieved injection, tool injection, authority downgrade, cross-session lookup, cross-task scope, and provenance access controls.

## 10. Determinism and concurrency

Determinism is required for parsing diagnostics, item IDs when the caller supplies a stable event ID, relationship ordering, scoring, packing, GC decisions, and assembly records. Time and random IDs are injectable in tests.

Concurrent ingestion must serialize conflicting mutations per task or use transaction retries. A successful operation is linearizable from the task’s perspective. Tests run with the race detector and cover concurrent duplicate ingestion, supersession, GC, and restart snapshots.

## 11. Delivery phases and exit gates

1. Domain and stores: enums, validation, scopes, authority, generations, relationships, memory store, SQLite schema, and transactions. Gate: unit, persistence, restart, graph integrity, and race tests.
2. Directives and ingestion: parser, IDs, defaults, diagnostics, normalization, classification, deduplication, and relationships. Gate: directive examples and injection tests.
3. Assembler: candidate filtering, mandatory partition, scoring, packing, provider-neutral messages, accounting, and explanations. Gate: budget property tests, overflow error, and deterministic records.
4. GC and archive: lifecycle transitions, TTL, stale scoring, deduplication, and supersession-aware collection. Gate: pinned/goal protection and auditability.
5. Retrieval and rehydration: lexical search, access tracking, retention feedback, and regret. Gate: archive recovery and explainable priority change.
6. Obligations: derivation, deterministic satisfaction, evidence links, and status history. Gate: unresolved visibility and proof-backed satisfaction.
7. Providers: fake, OpenAI-compatible, and Anthropic-compatible adapters. Gate: adapter contract tests preserve normalized semantics.
8. Replay and workload: baseline, managed runner, deterministic coding task, and large noisy outputs. Gate: both runners complete and emit metrics.
9. Evaluation and hardening: all test classes, security, profiling, and benchmark review. Gate: hard invariants pass and target results are reported.

## 12. Test specification

### Unit tests

Cover directive defaults and diagnostics; authority ordering; generation transitions; kind classification; relationship validation; provenance traversal; supersession cycles; obligation transitions; score calculation; stable ties; budget overflow; token accounting; search; rehydration; and regret metrics.

### Property tests

Prove that assembly never exceeds usable budget, GC never archives a pinned item or active goal automatically, no assembly contains another session’s items, supersession remains acyclic, derived items have valid provenance, identical seeded inputs assemble identically, and valid event replay reconstructs the same state.

### Integration tests

Prove SQLite restart recovery, concurrent ingestion integrity, authority preservation through storage and assembly, fake-provider end-to-end flow, archive/rehydration, obligations, and provider adapter contracts. Run all concurrency tests under the race detector.

### End-to-end benchmark

Use a deterministic dependency migration task with repository reads, architecture docs, dependency metadata, large build failures, source edits, test failures, changelog inspection, correction, passing tests, and final response.

Compare BASELINE (full transcript each call) with MANAGED (Context Runtime assembly each call). Record correctness, input tokens, peak active context, reduction, tool calls, model calls, rehydrations, pruning regret, latency, and invariant violations.

Initial target: at least 50% active-context reduction with no correctness degradation. Hard targets: zero lost pinned requirements, zero lost goals, zero budget violations, zero cross-session leakage, and zero provenance corruption.

## 13. Inspection contract

Expose a text or structured inspection view equivalent to /context with active goal, pinned items and obligation statuses, working items, durable items, archived counts, token usage, recent GC decisions, and unresolved requirements. Inspection is read-only and uses runtime authorization and scope checks.

## 14. Acceptance checklist

V1 is accepted only when:

- Authorized directives are parsed and persisted with source metadata.
- Goals and pinned requirements survive every automatic GC path.
- Tool and retrieved content cannot create privileged directives.
- Evidence, knowledge, state, provenance, and supersession are separate and queryable.
- Unresolved obligations are visible and evidence-backed satisfaction works.
- Archived evidence is searchable and rehydratable.
- Rehydration and pruning regret are measurable.
- Every assembly has deterministic inclusion/exclusion explanations.
- Mandatory context never silently disappears and token budget is never exceeded.
- Session and task isolation is enforced.
- Restart and concurrent operations preserve valid state.
- All provider adapters pass contract tests.
- Replay compares baseline and managed execution.
- The benchmark meets hard invariants and reports the reduction target.

## 15. ADRs required before affected phase exits

1. Go module path and minimum Go version.
2. Tokenizer strategy and provider-specific accounting.
3. SQLite driver and migration mechanism.
4. Stable ID format and event ID requirements.
5. Whether Pinned always implies Mandatory.
6. Scope containment matrix.
7. Lexical index and normalization rules.
8. Obligation matchers and false-positive handling.
9. Provider transport libraries and retry policy.
10. Benchmark fixture and correctness oracle.

Each ADR records the decision, alternatives, compatibility impact, and tests that lock the behavior.

