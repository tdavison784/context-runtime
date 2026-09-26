# Context Runtime: Spec Driven Development

Status: proposed

Version: 0.10

Date: 2026-09-26

## 1. Purpose

Context Runtime is a provider-aware context runtime for LLM agents. It sits between an agent harness and an LLM provider and manages semantic memory, task state, provenance, memory lifecycle, and model-specific context optimization. Garbage collection, memory, and provider optimization are features; the product is the context control plane.

Most harnesses treat conversation history, agent memory, and the next model context as the same thing. This runtime keeps them separate:

- Audit history: everything that happened. The transcript is immutable.
- Semantic state: what the agent currently knows, is trying to accomplish, and must keep satisfying.
- Model context: what one particular model call receives.

A smaller prompt is not automatically cheaper, faster, more correct, better cached, or compatible with the model's earlier reasoning. Rebuilding context from scratch can destroy prompt-cache reuse, invalidate reasoning continuity, duplicate provider-native compaction, and raise uncached-token cost. The runtime therefore optimizes effective task execution, not token count.

This specification is the V1 implementation contract: behavior, interfaces, invariants, persistence, testing, and delivery gates. The [normative event traces](docs/sdd-event-traces.md) supply acceptance cases for the contracts. Appendix A is non-normative.

### Architecture

    harness events, agent tool calls
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

Semantic state has three producers: directives from users and harness authors (FR-DIR), runtime-provided tools the agent calls to record facts, decisions, state, and checkpoints (FR-TOOL), and deterministic ingestion rules (FR-REL-007). The semantic layer never calls a model; provider calls go through the call ledger (FR-CALL), driven by the harness.

### Terminology

- Model call: one inference request whose assembly has been prepared and dispatched. Compaction requests are separate provider operations, with their own budgets, call records, and usage.
- Turn: begins when a USER event or a HARNESS turn-boundary event is ingested for a task and ends at the next such event for that task, so harness-driven tasks with no USER events still advance turns. A turn usually contains many model calls. TURN scope and TTLTurns count turns. The turn-opening message is the USER or HARNESS message that began the current turn.
- Pending input: the ingested items the next model call must answer: the turn-opening message, or the tool results that complete the open tool round.
- Exchange group: items a provider requires to travel together: an assistant message, the tool calls it contains, their tool results, and any provider reasoning items attached to that assistant message.
- Open tool round: an exchange group whose tool results have not yet been acknowledged by a successfully recorded model response. Sending a request alone does not close the round.
- Semantic state: items, relationships, lifecycle state, and obligations. It is provider-independent.
- Context Plan: the planner's provider-independent statement of what the next model call needs (FR-PLN-001).
- Checkpoint: a summary item, produced by the agent through context_checkpoint or by the harness, that covers every exchange group before it in its conversation (FR-TOOL-004). A rebase renders the newest checkpoint in place of the groups it covers.
- Materialization: turning a Context Plan into a provider request.
- Capability descriptor: versioned data describing a (provider, model): limits, pricing, caching, reasoning rules, edit safety, and native context features (FR-CAP-001).
- Edit safety: for each kind of history edit, whether the provider accepts it unchanged (SAFE), accepts it while dropping or ignoring reasoning (LOSSY), or rejects it (REJECTED) (FR-CAP-002).
- Epoch: a run of model calls bound to a session, task, agent, authorization context, provider configuration, and model. History grows by appends and declared safe edits. A rebase starts a new epoch; eligibility changes can require one even when the cache is warm.
- Access boundary: the immutable session and ownership constraints that govern who can read an item, including from the archive.
- Context eligibility: whether an authorized item may automatically enter the next model call. Scope, turn, task status, TTL, currentness, and residency affect eligibility without erasing archival access.
- Residency: RESIDENT or ARCHIVED; independent of a goal's OPEN or RESOLVED status and of supersession.
- Retrieval lease: explicit, audited permission to expose an authorized historical item as evidence to a particular task, agent, and turn for a bounded number of calls. It does not restore the item's instructions or currentness.
- Semantic pressure: resident semantic bytes above a policy limit independent of the provider. Provider pressure: the materialized request approaching its usable budget.
- Effective task cost: the currency cost of completing a task (FR-COST-001).
- Principal: the session, workflow, task, and agent a caller acts for, plus the caller's authority. Every API call carries one.
- Sequence number: a per-session, monotonically increasing number assigned when an event commits. Semantic recency uses this logical clock. Recorded elapsed-time observations are separate inputs to provider cache forecasts; replay never consults the live clock.
- Token counter: the component that counts serialized provider input for a (provider, model) pair. A counter is exact, or estimating with a declared upper error bound.

## 2. Goals and non-goals

### Goals

- Parse authorized context directives from user and harness input, and accept semantic state from the agent through runtime-provided tools.
- Preserve source authority and prevent lower authority from promoting itself.
- Track goals, constraints, knowledge, evidence, state, obligations, provenance, and supersession separately from the raw provider conversation.
- Produce a provider-independent Context Plan for every model call and materialize it with a strategy chosen from provider capabilities.
- Select requests using an estimated effective-cost policy while preserving goals, constraints, required state, configured reasoning continuity, and correctness. Validate savings against actual task outcomes; the policy makes no claim of optimal task cost.
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
- Summaries generated by the semantic layer. Summaries come from the agent through the checkpoint tool or from the harness, with coverage provenance (FR-TOOL-004, FR-REL-008); the semantic layer never calls a model. Provider-native compaction runs on the provider, through a strategy (FR-MAT-002).
- LLM-based classification, relationship detection, or obligation matching.
- Content redaction and deletion. Stores are treated as secret-bearing (section 9).

Interfaces may leave room for these, but V1 must prove runtime semantics first. The expected post-V1 direction is a context service (contextd) behind a language-neutral Context Protocol, so agent frameworks in Go, Python, and TypeScript can share the runtime.

## 3. Functional requirements

Requirement IDs are stable and are part of the test contract. New requirements take the next free ID in their group; removed IDs are not reused.

### Ingestion and authority

- FR-ING-001: Every event enters one pipeline: check event identity; normalize and snapshot content; assign authority and access boundary per span; parse directives within authorized spans; classify (a directive's kind wins over heuristics); apply generation, scope, and retention defaults; deduplicate; validate relationships and authorized transitions; assign a sequence number; persist atomically. External content acquisition happens before the transaction. Provider token counts are lazy, versioned caches populated during materialization, not prerequisites for semantic ingestion.
- FR-ING-002: Authorities are SYSTEM, HARNESS, USER, AGENT, TOOL, and RETRIEVED_CONTENT. Precedence is SYSTEM > HARNESS > USER > AGENT > TOOL = RETRIEVED_CONTENT. TOOL and RETRIEVED_CONTENT cannot override each other. Where a total order is required (FR-ASM-006), TOOL sorts before RETRIEVED_CONTENT.
- FR-ING-003: An event may not raise its own authority, generation, pinned state, scope, retention, or mandatory status. Only an authorized source or runtime policy may grant those properties. Span authority cannot exceed the principal's authority.
- FR-ING-004: Directive parsing applies to SYSTEM and HARNESS spans, and to USER spans the harness marks as directive-capable: instruction files, an explicit command, or a dedicated input field. Ordinary chat text is not parsed unless the harness marks it, because pasted documents commonly contain headings such as "## Goal". An event may carry several spans; the harness marks embedded content it knows about (attachments, file contents, echoed tool output) as TOOL or RETRIEVED_CONTENT spans. Directive headings inside fenced code blocks or block quotes are never parsed. Tool output and retrieved content remain content even when they contain directive-looking headings. Each span, and each separate text part within a span, is an isolated parse unit: fenced/quoted/comment suppression state never carries across a span or part boundary, adjacent spans or parts are never concatenated into one directive token, and an image or document part interrupts text parsing.
- FR-ING-005: Stable source identity, content hash, or explicit relationship data must make duplicate events detectable while preserving audit references. Duplicates are linked with DUPLICATE_OF, never merged, and a duplicate never creates a SUPERSEDES edge even when it reuses a directive ID or key. Deduplication never crosses session, scope, or authority boundaries, and a duplicate never inherits the canonical item's authority, generation, retention, or mandatory status. A Working snapshot's duplicate identity is the complete snapshot membership and its members' effective metadata; equal member text in an otherwise changed snapshot does not by itself make that member a duplicate. Ordinary identical restatement compares the immutable creation declaration and does not reopen, re-pin, unarchive, or rebind an obligation; TURN/TTL eligibility origin remains part of semantic identity. An explicit authenticated ActionReplaceDirective intent, naming the expected current occurrence and version under CAS with its own idempotent request identity, may reuse identical content and is audited with all indirect obligation retirements.
- FR-ING-006: A caller-supplied EventID is an idempotency key within a session. Repeating it with the same canonical payload, principal, and source returns the original result without a new sequence number, transition, or usage increment. A different payload or principal fails with ErrEventIDConflict. Content deduplication under different event IDs remains a separate operation. Durable harness integrations must supply stable IDs for retries.
- FR-ING-007: Text, images, and documents are immutable snapshots addressed by content hash. Paths and URLs are locators, not snapshots. The harness supplies the bytes or an immutable blob reference whose bytes and hash the store verifies before acceptance; the runtime does not fetch arbitrary locators itself. A changed resource creates new evidence. Counter caches include content hash, part encoding/options, counter version, and serializer version; whole-request counts include framing and tools. Mutable external content is never dereferenced during replay or resend.

### Mutation authorization

- FR-AUTH-001: Every mutation checks both access to its targets and authority for the action, including indirect changes. Resolve, Unpin, directive replacement, scope changes, obligation assertions, blocking, unblocking, and waiver require a SYSTEM, HARNESS, or USER principal with authority at least equal to each affected source, or the action-specific grant in FR-AUTH-002. Unauthorized operations fail atomically with ErrInvalidAuthorityPromotion. An immutable item's access boundary cannot be widened in place.
- FR-AUTH-002: A source may authorize a specific runtime matcher version to evaluate its obligation, or delegate a named action to a SYSTEM, HARNESS, or USER principal on named targets within a scope and expiry. Only a principal with authority at least equal to the affected source may issue or revoke that grant. Matcher transitions run under that recorded grant; low-authority tool evidence does not itself mutate status. Positive matcher transitions require a live exact-obligation-version grant. Delegation cannot change source authority, let a lower-authority actor grant itself authority, or grant archival access beyond its boundary. AGENT, TOOL, and RETRIEVED_CONTENT cannot receive lifecycle mutation grants in V1. Runtime invalidation of an already accepted resource-bound proof is a restricted audited consequence of the recorded resource/proof change, permitted after the original grant expires or is revoked; it confers no general mutation or disclosure authority.
- FR-AUTH-003: CompleteTask requires a SYSTEM, HARNESS, or USER principal authorized to resolve every task-owned open goal. It rejects unresolved or blocked current obligations with ErrUnfinishedObligations. It then atomically marks the task complete, resolves those goals, and requests GC. It never waives obligations or resolves goals owned by another scope. Failure changes nothing. Authorized historical reads remain possible after completion.

### Directives

Directives are parsed by the runtime. They are lifecycle instructions to the runtime, not text suggestions to the model.

- FR-DIR-001: V1 recognizes the content sections Goal, Pinned, Working, Remember, References, and Ephemeral, and the lifecycle commands Resolve and Unpin.
- FR-DIR-002: Directive IDs use [id] syntax and are stored independently from display text. Each (session, task, access boundary, directive ID) has one current version. A version in a boundary the writing principal cannot access neither blocks nor reveals itself through a shared ID; but a write that reuses an ID while the writer can access a current version of it in a different boundary is rejected, because boundary changes cannot be made through ID reuse (below). An item without an ID receives a derived ID (a lowercased content-section keyword, a hyphen, and the 64 lowercase hex digits of its content hash), reported in diagnostics and inspection. Reusing an ID creates a new item and atomically SUPERSEDES the current version, subject to FR-AUTH-001 and FR-REL-006. Only the current version may impose requirements. Scope or access-boundary changes require an explicit authorized replacement policy; they cannot be smuggled through ID reuse. An explicit [id] that has the derived-ID shape (a lowercased content-section keyword, a hyphen, and 64 lowercase hex digits) is rejected with an ErrMalformedDirective diagnostic and the item is dropped, never assigned a derived ID, so explicit and derived IDs can never collide. Only the six content-section keywords (Pinned, Working, Remember, Ephemeral, References, Goal) derive IDs this way; a lifecycle keyword (Resolve, Unpin) never derives one, so `resolve-<64hex>`/`unpin-<64hex>` are ordinary explicit IDs, not derived-ID-shaped rejections.
- FR-DIR-003: Defaults are:
  - Goal: goal kind, durable generation, task scope, GoalStatus=OPEN, PROTECTED retention and mandatory while current and eligible.
  - Pinned: constraint kind (instruction via kind=), pinned generation, task scope, PROTECTED retention, mandatory.
  - Working: task_state kind (conversation via kind=), working generation, task scope, NORMAL retention.
  - Remember: fact kind (decision or summary via kind=), durable generation, task scope, HIGH retention.
  - References: reference kind, working generation, task scope, NORMAL retention. Each item names an item ID, a path, or a URL. A name that matches an item ID in scope, or the source path of an ingested item, gets a REFERENCES edge; a path not yet read is linked when an item with that source path is ingested later.
  - Ephemeral: evidence kind (tool_result via kind=), ephemeral generation, TURN scope, LOW retention.
  New content is RESIDENT by default; residency changes do not change semantic status.
- FR-DIR-004: Parser output includes source spans, the directive section, directive ID, source authority, parsed scope, parsed TTL, and diagnostics. Malformed directives do not discard unrelated content.
- FR-DIR-005: Resolve sets the current goal's GoalStatus to RESOLVED, stops its mandatory status, and drops its retention to HIGH without changing residency. Unpin moves the current pinned version to DURABLE with HIGH retention; it does not waive an attached obligation. Both follow FR-AUTH-001, are audited, and produce a diagnostic for an unknown ID. A target is an item ID, or a directive ID that resolves to exactly one current version the principal can access; a directive ID with several accessible current versions (possible when they were written by principals who could not see each other) produces an ErrAmbiguousDirective diagnostic and mutates nothing, and the principal must name the item ID. Reopening a resolved goal requires an authorized replacement with a new OPEN version: ordinary identical restatement compares the immutable creation declaration and never reopens, re-pins, unarchives, or rebinds an obligation by itself, but an explicit authenticated ActionReplaceDirective intent may reuse identical content under CAS on the expected current occurrence/version, auditing every indirect obligation retirement. Other lifecycle words produce ErrUnsupportedDirective diagnostics rather than ambiguous mutations. The unsupported-lifecycle vocabulary recognized for this diagnostic is Archive, Unarchive, Promote, Demote, Block, Unblock, Waive, CompleteTask, and Reopen; other headings are not lifecycle commands at all and remain ordinary content or DirectiveNotParsed diagnostics under FR-DIR-006.
- FR-DIR-006: Directives follow this grammar (ABNF):

      section    = marker SP keyword [SP "[" id "]"] *(SP attr) EOL body
      marker     = 1*6"#"
      keyword    = "Goal" / "Pinned" / "Working" / "Remember" / "References"
                 / "Ephemeral" / "Resolve" / "Unpin"
      body       = item-list / text   ; ends at same/higher heading, span end, or EOF
      item-list  = 1*item
      item       = bullet SP ["[" id "]" SP] ["{" attr *(SP attr) "}" SP] text *continuation
      bullet     = "-" / "*" / 1*DIGIT "."
      id         = 1*80(ALPHA / DIGIT / "-" / "_" / ".")   ; fits derived IDs (FR-DIR-002)
      attr       = attr-name "=" value
      attr-name  = "kind" / "scope" / "ttl" / "obligation"
      value      = 1*(ALPHA / DIGIT / "-" / "_" / ".")

  A section whose body is a top-level list yields one item per list item; indented continuation lines belong to their item. Otherwise the body is one item and may take its id from the heading; a heading id on a list section is a diagnostic. Section attributes apply to every item, and item attributes override them. Keywords match case-insensitively; everything else is exact. A heading whose first word is not a keyword is ordinary content.

  Attributes are allowed as follows: kind on Pinned, Working, Remember, and Ephemeral; scope on all content sections; ttl (a positive count of turns) on Working, Remember, References, and Ephemeral; obligation on Pinned. ttl accepts ASCII decimal digits (leading zeros allowed) with a value from 1 to 2147483647 inclusive, parsed with checked arithmetic; a syntactically valid but larger value fails event validation with a representation-limit error rather than being ignored or treated as unlimited. scope= may widen scope beyond TASK only in SYSTEM or HARNESS spans. Resolve and Unpin take target IDs from the heading or from list items with no text. Unknown attributes, invalid values, and disallowed attributes produce diagnostics and are ignored.

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
- FR-DIR-007: A Working section is a snapshot of current state. Ingesting one supersedes every current Working-section item (identified by its recorded directive section, whatever its kind) of the same authority and access boundary in the same task (FR-REL-006), with SUPERSEDES edges from the new items to the old ones, under FR-AUTH-001 and FR-REL-006. Items with explicit IDs also supersede by ID (FR-DIR-002).

### Semantic state tools

The runtime provides provider-neutral tool definitions and handlers that the harness registers with the model. They are how the agent writes semantic state and reads the archive. Tool calls arrive through the harness with AGENT authority and the harness-supplied principal; the harness never maps a tool call to a higher principal, and these tools never receive lifecycle mutation grants (FR-AUTH-002).

- FR-TOOL-001: The V1 tool set is context_search(query), context_get(id), context_rehydrate(id), context_remember(key, kind, text, evidence_ids), context_update_state(key, text, evidence_ids), context_resolve(id, evidence_ids), and context_checkpoint(summary). Names, schemas, and result formats are fixed by ADR 18 and are identical across providers. The retrieval tools follow FR-RET-001 and FR-RET-006.
- FR-TOOL-002: context_remember creates a fact or decision; context_update_state creates a task_state item. Both are keyed: the item ID is "agent." followed by the key, within task scope, so agent keys never collide with directive IDs. A write to an existing key creates a new item that SUPERSEDES the previous one at the same authority. Cited evidence IDs must be accessible to the principal and become DERIVED_FROM edges with coverage under FR-REL-008; an inaccessible ID is a tool error and nothing is written. Items written without evidence are allowed and are marked unsupported in decision traces, which lowers their dependency value. The semantic key is agent.<key> in AGENT_KEY, scoped to session, task, agent and exact ownership boundary. Each version has a distinct immutable item occurrence ID. Identical semantic writes create duplicate occurrences without supersession; unsupported status derives from qualifying cited-evidence support, not from the mere absence of any DERIVED_FROM edge.
- FR-TOOL-003: context_resolve never resolves in V1, because Resolve requires a SYSTEM, HARNESS, or USER principal (FR-AUTH-001) and the tools cannot create goals. For an accessible goal it records a completion claim: an evidence item DERIVED_FROM the cited evidence, linked to the goal with REFERENCES. The result reports the referenced goal occurrence's actual observed status and current/historical classification, and states that the tool changed neither the goal nor its obligations; for a current OPEN goal it also states that an authorized Resolve or CompleteTask is needed. An unknown or inaccessible ID is a tool error. Obligations are satisfied only through matchers or authorized assertions (FR-OBL-004), never through this tool.
- FR-TOOL-004: context_checkpoint creates a summary item covering every exchange group before it in its conversation; its provenance is that coverage range (FR-REL-008), and its access boundary is the conversation's. A checkpoint larger than a policy fraction of the usable budget (ADR 5) is rejected with a tool error asking for a shorter summary. The harness may also ingest a checkpoint from its own model call, with HARNESS authority and an explicit coverage range. Because Plan and Assemble are previews that report the proposed mode, the harness can request a checkpoint before preparing a call that would rebase. Checkpoint source provenance and the complete closed exchange prefix it may replace are recorded separately (ADR 6). Membership is explicit; current requirements and pending/open exchanges cannot be removed by coverage. Authored semantic knowledge may outlive source expiry; raw/opaque/retrieval representations retain their source and exact lease dependencies.
- FR-TOOL-005: The runtime ships a reference instruction block: provider-neutral text that tells the agent when to record facts, decisions, and state, when to cite evidence, when to resolve, and to checkpoint before and after long tool sequences. The harness ingests it as a HARNESS-authority instruction. Its wording is versioned with the policy because it changes results, and the benchmark fixture uses it (section 12).

### Domain model

- FR-DOM-001: Every item has an immutable ID; session, workflow, task, agent, and turn IDs; access boundary; kind, generation, authority, scope, residency, optional goal status, retention class, immutable content parts, content hash, semantic byte size, sequence number, audit timestamps, usage counters, source reference, and event and directive IDs when applicable. Per-counter token counts are derived caches and never semantic scoring inputs.
- FR-DOM-002: Kinds include goal, constraint, instruction, user_message, assistant_message, conversation, fact, decision, task_state, tool_call, tool_result, error, artifact, reference, summary, evidence, and reasoning. A reasoning item is an audit reference to an opaque provider block bound to its provider, model, and epoch; it is excluded from semantic candidate selection. Obligations are companion records (FR-OBL-001), not items.
- FR-DOM-003: Scopes are TURN, TASK, WORKFLOW, SESSION, and AGENT. Access always requires the same session; TURN and TASK also require the same task, WORKFLOW the same workflow, AGENT the same agent, and SESSION permits principals in that session. Context eligibility additionally requires: TURN, the current turn of an active task; TASK, that active task; other scopes, their matching active owner; and an unexpired TTL/lease. V1 registered WORKFLOW and AGENT owners remain active for the session lifetime; the absence of active child tasks does not end their scope. Unknown required owner state forbids automatic admission. Archived residency prevents new automatic selection, but does not alone prohibit retaining already rendered authorized history (FR-MAT-004). Historical goals/directives cannot be selected as current requirements. A historical retrieval lease can admit authorized evidence without restoring instructions. Access, temporal eligibility, and automatic selection are distinct checks; ADR 6 fixes their matrix before Phase 1 exits.
- FR-DOM-004: Generations are PINNED, DURABLE, WORKING, and EPHEMERAL. An item is pinned exactly when its generation is PINNED; there is no separate flag. Generation drives retention policy, not how a provider receives the item. Promotion and demotion are explicit, audited transitions.
- FR-DOM-005: Residency is RESIDENT or ARCHIVED. GoalStatus is OPEN or RESOLVED for goals and absent for other kinds. Currentness is derived from SUPERSEDES relationships and the current directive-version map. Expiry is derived from turn, task, and TTL state; rehydration is derived from retrieval events and leases. These dimensions are independent: a RESOLVED goal may be RESIDENT for inspection without becoming an active objective. Archiving and rehydration never change GoalStatus or currentness.
- FR-DOM-006: Evidence, knowledge, and state are separate semantic categories. Derived knowledge points to evidence; state changes point to the observations or decisions that caused them. For example, a 30K-token compiler output (evidence) supports the fact "the FooClient constructor now requires context.Context" (knowledge, DERIVED_FROM the output), which drives the task state "3 call sites need migration". The output can archive, the fact stays durable, and the task state stays active until superseded. Kinds map to categories:
  - Directive: goal, constraint, instruction.
  - Knowledge: fact, decision, summary, reference.
  - Evidence: tool_result, error, evidence, artifact. The evidence kind covers evidence not produced by a tool, such as a harness assertion or a user-supplied log.
  - State: task_state.
  - Conversation: user_message, assistant_message, conversation, tool_call, reasoning.
- FR-DOM-007: Mandatory status is derived at planning time. First enforce access and context eligibility. Among current versions, system policy (SYSTEM-authority instructions), security constraints, OPEN goals, PINNED requirements, and current UNRESOLVED or BLOCKED obligations are mandatory; FR-OBL-003 governs any authorized materialization exception. The turn-opening message, pending input and its logical tool exchange, and, at a rebase, the conversation's newest checkpoint (FR-TOOL-004) are mandatory regardless of scoring. Historical/superseded requirements are never mandatory by virtue of their old kind or generation. Explicitly requested historical evidence may be pending input but must render as historical data. Retention cannot increase source authority.
- FR-DOM-008: Content, kind, authority, IDs, scope, access boundary, source, and sequence number are immutable. Generation, residency, goal status, retention class, usage counters, and version change only through audited lifecycle events.
- FR-DOM-009: SemanticBytes is the byte length of a versioned canonical representation of text and typed metadata plus the byte lengths of referenced immutable blobs, counted once per part. It uses no model tokenizer. The semantic policy uses only this unit for size penalties and resident-memory pressure. Provider reasoning and compaction blocks belong to the conversation's opaque provider state; their payloads, token counts, and cache timings never enter semantic scoring. The reasoning kind is an audit reference to that state, not a planner candidate.

### Provenance, supersession, and obligations

- FR-REL-001: Relationship records store item-to-item DERIVED_FROM, SUPERSEDES, DEPENDS_ON, REFERENCES, and DUPLICATE_OF edges. SATISFIES is a typed derived relation from authoritative obligation-transition and proof records to an obligation version; it is not a separately persisted item-to-item edge. Items do not duplicate an item-to-item edge's data.
- FR-REL-002: A derived item cannot be persisted with missing required provenance. Dangling relationship IDs are rejected with ErrDanglingRelationship.
- FR-REL-003: Supersession marks obsolete state so it is no longer current truth, while retaining nodes and edges for audit. Comparable test states "29 failing", "7 failing", "1 failing", and "PASS" may form a chain with only PASS current. Historical requirements cease to be mandatory atomically with replacement. Old rendered state must be identified as obsolete before the next inference through an authorized semantic delta or rebase (FR-MAT-004).
- FR-REL-004: Supersession is acyclic. A cycle is a transaction error.
- FR-REL-005: A “why do we believe this?” query traverses provenance to evidence under the same authorization and scope checks. Nodes the principal cannot see are omitted and the path is marked truncated.
- FR-REL-006: A SUPERSEDES edge requires access to both endpoints, an authorized mutation, and superseding authority at least equal to superseded authority. Automatic rules operate only within the same access boundary and semantic subject. A lower-authority or cross-boundary attempt fails; it cannot suppress an item visible to another principal. Source authority cannot be laundered through derivation or replacement.
- FR-REL-007: V1 creates relationships from explicit authorized event data, directive replacement, keyed agent writes (FR-TOOL-002), Working snapshots (FR-DIR-007), and versioned deterministic rules. A tool_result DEPENDS_ON its tool_call. Tool-name/argument equality alone never creates SUPERSEDES. A trusted observation envelope identifies subject, repository/resource identity, working directory, environment, revision/content fingerprints, coverage, outcome, and matcher version. Registered rules may derive comparable current-state items from those observations and supersede their previous state. The original observations remain immutable evidence. Timeouts, partial output, and independent effectful operations do not imply replacement.
- FR-REL-008: Derived content, retrieval projections, and opaque provider state carry source coverage. Their access boundary is no broader than the intersection of their sources' boundaries; missing source coverage prevents transmission. A retrieval projection inherits its lease's eligibility limits. Broadly scoped summaries cannot expose narrower evidence through copied text. V1 provides no automatic declassification; a separately authorized new assertion must not misrepresent restricted source content as public provenance.
- FR-OBL-001: An obligation has a stable ID, description, status, scope, owner/source, creation time, matcher, and satisfying evidence IDs. In V1, an obligation is created from a Pinned item with obligation=<matcher>, from a HARNESS event that declares one, or when a registered matcher's deterministic claim pattern matches a Pinned item's text. For example, the file_read matcher claims "Read <path>" and is satisfied by a read of that path; tests_pass claims "All tests must pass" and is satisfied by a passing test run. An explicit attribute wins over claim patterns. The runtime never uses a model to infer obligations.
- FR-OBL-002: Statuses are UNRESOLVED, SATISFIED, BLOCKED, and WAIVED. Status history is append-only. Allowed transitions:
  - UNRESOLVED to SATISFIED: an explicitly authorized matcher accepts applicable evidence, or an authorized assertion under FR-AUTH-001. Evidence IDs, applicability fingerprints, and the evaluated obligation version are recorded.
  - SATISFIED to UNRESOLVED: the authorized matcher invalidates stale/rejected evidence under FR-OBL-005, or an authorized assertion requests revalidation.
  - UNRESOLVED to BLOCKED and back: an authorized lifecycle action under FR-AUTH-001.
  - Any status to WAIVED: an authorized waiver under FR-AUTH-001. WAIVED is terminal for that obligation version.

  AGENT, TOOL, and RETRIEVED_CONTENT cannot change obligation status. Other transitions fail with ErrInvalidTransition.
- FR-OBL-003: Current unresolved and blocked obligations are mandatory task state unless their source or an equal/higher-authority policy explicitly disables materialization. That exception is recorded and does not satisfy, waive, or permit completion of the obligation.
- FR-OBL-004: Tool evidence satisfies an obligation only through its deterministic matcher or an authorized harness assertion. Matchers and their claim patterns are registered by name and versioned with the policy.
- FR-OBL-005: Each matcher declares evidence applicability and invalidation dependencies. tests_pass requires a successful complete run of the declared suite at the current workspace fingerprint, including relevant uncommitted changes, configuration, and dependencies. A harness resource-change event invalidates affected satisfaction before the next plan, even without another test run. If freshness cannot be established, status is UNRESOLVED. file_read binds to the path and content version required by the obligation. Historical evidence remains linked after invalidation; a newer valid proof replaces the current proof through an audited transition. Harnesses must report relevant resource changes; correctness of unreported external changes is outside the runtime's guarantee. The current resource state is established by authenticated ordered resource reporting, not by receipt of an observation. Assertions explicitly distinguish authority attestation from resource-bound proof. Newer applicable rejected proof and proof refresh use the defined audited transitions.
- FR-OBL-006: Replacing a directive retires its associated obligation version from the current mandatory set. A replacement obligation gets a new version and starts UNRESOLVED; prior proof carries over only after its matcher explicitly revalidates applicability. The old status and proof remain auditable. Unpin alone does not retire or waive an obligation, and reading historical proof does not alter status.

### Assembly

Assembly is planning (what the call needs) followed by materialization (how to send it).

- FR-ASM-001: Assemble(principal, request) takes pending input IDs, provider configuration/model, token budget, output reservation, reasoning-continuity policy, and tool definitions. It reads a consistent semantic and conversation version and returns normalized messages, selected item IDs, a content-addressed assembly ID, proposed epoch/mode, per-item manifest, token accounting, estimated cost, and decisions. It is a preview: it does not send requests or advance usage, calls, or epochs. If native preparation is needed it returns an explicit compaction proposal instead of an inference-ready request; FR-MAT-005 defines the continuation. Pending input must already be ingested, and the tool results for an open round must be complete, or assembly fails with ErrIncompleteToolRound.
- FR-ASM-003: For each provider operation, usable input budget is min(configured total-token budget, model context window) minus the adapter's output/reasoning reservation and counter safety margin. Reserve reasoning separately only when the provider's output limit does not already include it. The registered counter counts the submitted input after serialization, including system content, tools, framing, pending input, and opaque blocks, before any new server transformation. Counted input must fit usable budget; estimated counts require a documented upper error bound covered by the margin, otherwise strict-budget requests fail with ErrUnboundedTokenEstimate. Existing provider compaction receipts may determine effective counts, but anticipated compaction never discounts an oversized submission. Record submitted count, effective model-visible count, and all billed iteration counts separately. Validate reservation/output limits and reject a non-positive usable budget.
- FR-ASM-004: Mandatory items are never silently discarded or truncated. If they exceed budget, return ErrMandatoryContextExceedsBudget with required/available counts and item IDs, so the caller can Unpin, Resolve, or split the task.
- FR-ASM-007: Every assembly is explainable for all authorized candidates and every inherited provider segment. Semantic decisions report eligibility, currentness, mandatory status, semantic size, score factors, and closed-set reason codes. Materialization decisions separately report selected, omitted, retained historically, replaced, or represented by an opaque block, with source IDs, provider token accounting, source authority, and why. An append record may store a delta plus an immutable parent record, but ExplainAssembly reconstructs complete membership without reading mutable current state. A semantic exclusion must never be reported as a provider omission if its bytes remain in history. Policy (ADR 5) may store the long tail of excluded candidates as per-reason-code aggregates with the top-ranked entries per code kept in full; membership, inherited segments, and every included item's reasons are always complete.
- FR-ASM-012: Before its first transmission, a tool result may receive a bounded delivery projection under an explicit harness policy. Archive the full immutable observation, retain its hash and provenance, and emit a tool-result projection with call ID, outcome, truncation notice, evidence ID, and retrieval route. The raw blob is evidence supporting the delivered result, not another mandatory message. This policy never abbreviates goals, pins, user instructions, or an already transmitted exchange. If the configured projection plus its mandatory exchange does not fit, return ErrMandatoryContextExceedsBudget without dispatch; the harness must supply a smaller authorized projection or split the tool operation.

### Context planning

- FR-PLN-001: Before every model call, the planner produces a Context Plan from semantic state: the pending input, mandatory items, relevant items (ranked), optional items (ranked), retrievable archived items worth a stub, the conversation's newest checkpoint and the exchange groups it covers, and obsolete items (superseded or expired) that may still be in the rendered history. The plan states what should be available, not how to render it.
- FR-PLN-002: The planner is provider-independent. Given the same principal, semantic snapshot, pending input, logical call/turn indexes, and semantic policy, it produces the same plan for every provider. It reads neither capability descriptors, provider conversation, per-counter token caches, nor cache timings. Logical exchange dependencies comprise tool calls and their delivered results; provider-specific reasoning/compaction attachments are handled during materialization.
- FR-PLN-003: Relevant items clear the policy's relevance threshold. Optional items fall below it; a strategy may include them when budget and cost allow.
- FR-PLN-004: Exchange groups covered by the conversation's newest checkpoint are marked covered. At a rebase they are excluded by default with the covered reason code and remain retrievable (FR-RND-004); a strategy may still include a covered group that ranks as relevant when budget allows. In append mode, covered groups already in history stay under FR-MAT-004.
- FR-ASM-002: The planner partitions candidates into mandatory items (FR-DOM-007) and non-mandatory items. All mandatory items are included or assembly fails (FR-ASM-004). Non-mandatory items are ranked by score (FR-ASM-005); generation contributes to the score but is not a hard tier. Policy v1 weights generation so that, other factors equal, active task state ranks above durable knowledge, then working memory, rehydrated items, and ephemeral evidence. Selection order never determines render order (FR-RND-001).
- FR-ASM-005: Non-mandatory ranking uses a versioned explainable score. Factors: importance, authority, generation, lexical relevance, dependency value, recurrence, recency, state value, SemanticBytes size penalty, deterministic retrieval-effort class, duplication, and supersession. V1 relevance uses lexical overlap with pending input, the turn-opening message, and current OPEN goals under ADR 7. Recency uses sequence numbers and completed logical inference indexes. Scores are fixed-point integers. Provider-specific token and currency costs affect materialization, never this score.
- FR-ASM-006: Ties break by score (descending), authority (descending, FR-ING-002 order), last-used call index (descending), sequence number (ascending), then item ID (ascending).
- FR-ASM-008: Session, task, agent, and scope filters run before scoring. Cross-session items never enter the candidate set.

### Provider capabilities

- FR-CAP-001: Each provider configuration/model has a versioned descriptor tied to API surface, endpoint, account feature profile, required headers, and model version. It covers limits and reservations; counting method/error bound; caching semantics, minimum lengths, breakpoints and TTLs; pricing; reasoning replay/binding rules; safe edits; provider role mapping; native compaction protocol, minimum trigger, pause support, retained fields and returned blocks; context editing; mid-conversation system messages; and memory. OpenAI-compatible local servers have their own verified profiles; API-shape compatibility does not imply feature support.
- FR-CAP-002: Edit kinds are APPEND, APPEND_SYSTEM, ADD_DEFERRED_TOOL, MOVE_CACHE_MARKERS, DROP_LEADING_REASONING, DROP_ALL_REASONING, and REWRITE (any other change to earlier content; a REWRITE always drops the reasoning of the rewritten range and everything after it, because a fresh epoch never replays old reasoning). The descriptor marks each kind SAFE, LOSSY (accepted, but the provider drops or ignores reasoning), or REJECTED. A strategy performs SAFE edits freely, performs a LOSSY edit only as a deliberate reset under ALLOW_RESET (FR-ASM-011), and never performs a REJECTED edit. A binary safe/unsafe flag is insufficient because the same edit is rejected on one profile and silently lossy on another.
- FR-CAP-003: Descriptors are data, versioned, and selected per request. Provider- and model-specific checks live only in descriptors and adapters, never in the semantic runtime or planner.
- FR-CAP-004: Adapter contract tests verify each supported descriptor/profile with recorded request-response fixtures and scheduled live probes. They check role placement, valid tool exchanges, reasoning transformations, cache behavior under declared conditions, and native checkpoint/resume behavior. Unsupported features are never attempted in application traffic; negative probes are isolated tests. A successful request alone is insufficient evidence of reasoning preservation. Unknown or stale profiles fail closed for optional native operations and use only a previously verified baseline.

Informative provider evidence reviewed 2026-09-25: [Claude preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking) documents history binding and account/header-dependent enforcement. [Claude threshold compaction](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold) documents pause-and-resume support; [OpenAI compaction](https://developers.openai.com/api/docs/guides/compaction) documents both automatic compaction during inference and a standalone operation. Descriptors must verify the precise selected protocol. Automatic continuation alone does not provide a runtime checkpoint for restoring requirements. Under that binding, Claude Fable 5.1 and Claude Opus 5.5 profiles mark APPEND, APPEND_SYSTEM, ADD_DEFERRED_TOOL, MOVE_CACHE_MARKERS, DROP_LEADING_REASONING, and DROP_ALL_REASONING as SAFE and REWRITE as LOSSY (performed with all reasoning stripped or with the provider's drop setting; a rewrite that retains later thinking blocks is REJECTED on enforced accounts). [OpenAI reasoning](https://developers.openai.com/api/docs/guides/reasoning) and [conversation state](https://developers.openai.com/api/docs/guides/conversation-state) document that stateless Responses API requests must pass back reasoning items (encrypted content) with the function call and output items since the last user message, and that omitted reasoning items are ignored rather than rejected; those profiles mark REWRITE and DROP_LEADING_REASONING as LOSSY, so Rebuild is lossy on every call there and suits non-reasoning and local profiles instead.

### Materialization

- FR-MAT-001: A materialization strategy takes the Context Plan, descriptor, immutable conversation snapshot, request budget, principal/eligibility snapshot, and recorded forecast inputs. It is pure with respect to semantic and conversation state. It returns an inference-ready materialization or a compaction proposal, mode, proposed epoch, representation manifest, estimates for considered alternatives, and reasons. Network operations and state commits use the call lifecycle (FR-CALL-001).
- FR-MAT-002: V1 provides one strategy family parameterized by rebase policy. Its named configurations are:
  - Rebuild: rebase every call, materializing minimal context from semantic state. Suited to providers with weak caching, REWRITE-safe history, and no history-bound reasoning.
  - AppendOnlyCached: a stable prefix and append-only history within an epoch. It rebases only on the required triggers in FR-ASM-010 or when FR-COST-003 favors a rebase. This is the default for providers with prompt caching or history-bound reasoning.
  - ProviderNative: preserves valid history and uses a verified native checkpoint protocol under pressure. Policy opt-in is required. Actual returned blocks are recorded for exact replay; compaction content for an alternative history cannot be inferred by offline simulation.
- FR-MAT-003: Policy lists the strategies allowed for each (provider, model); when several are allowed, the cost model chooses (FR-COST-002). A strategy never requests a native operation the descriptor does not declare, never performs a REJECTED edit, and performs a LOSSY edit only as a deliberate reset under ALLOW_RESET (FR-ASM-011).
- FR-MAT-004: Representation may retain authorized historical evidence after semantic archival, but must immediately reflect changed requirements/current state before the next inference. Emit a versioned semantic delta at the affected item's original authority naming replaced/resolved/invalidated versions and their replacements; rebase if the provider cannot represent that delta safely. Rehydrated historical requirements are labeled evidence and never reissued as instructions. Loss of access, turn/task eligibility, or TTL eligibility requires removing affected content and any opaque state derived from it before dispatch; it cannot be deferred for cache savings. A retrieval lease can explicitly admit historical evidence within the same access boundary.
- FR-MAT-005: Native compaction is a checkpoint protocol: (1) prepare/count a permitted compaction operation; (2) send it through the durable call ledger; (3) persist the canonical returned blocks and their source coverage; (4) restore every current mandatory item, including pending exchanges and blocked obligations, at its source authority; (5) validate visibility, reasoning continuity, structure, and the whole-request count; (6) prepare a new inference from that checkpoint. Use a standalone or pause-after-compaction operation that cannot generate task actions before step 4. Automatic in-request compaction is allowed only with a descriptor-tested mechanism guaranteeing the full mandatory set remains effective throughout that response; summary instructions alone are insufficient. Otherwise it is disabled. If mandatory restoration would invalidate retained thinking, use a supported placement, perform a permitted deliberate reset, or fail with ErrReasoningContinuityRequired.
- FR-MAT-006: A trigger below the budget is a scheduling hint, not budget proof. Before adding a large pending exchange, compact already admitted history if needed and if the compaction request itself fits its operation budget. Then append pending input and restored requirements and recount. Bound retry/compaction attempts by policy; a non-shrinking result ends with an explicit error or an allowed rebase. The runtime never submits an oversized request in anticipation of server compaction.
- FR-ASM-009: Packing works on exchange groups: a group is included or excluded as a unit. An included item's required DEPENDS_ON targets are included too, or the item is excluded.
- FR-ASM-010: APPEND extends the committed conversation with eligible new input and semantic deltas; inherited segments are validated too. Manifests record every source ID/span, including copies inside raw message envelopes and the coverage union of opaque provider blocks. Rebase when no epoch exists, the principal/authorization context changes, inherited content loses access or turn/task/TTL/lease eligibility, the provider/model/tools/serialization profile changes incompatibly, a required delta cannot be appended safely, or budget pressure has no permitted native solution. Archival alone need not force a rebase. Policy may also choose a rebase when the rendered request exceeds its soft-pressure fraction of the usable budget (ADR 5), or a cheaper rebase under FR-COST-003. No opaque block may cross an ownership boundary merely because its contents cannot be inspected. Delta records require reconstructible parent manifests (FR-ASM-007).
- FR-ASM-011: Compatible reasoning blocks are replayed verbatim within an epoch, never into a fresh epoch. Continuity policy is REQUIRE or ALLOW_RESET, chosen by the harness and recorded per call. REQUIRE rejects any reset that would discard required reasoning with ErrReasoningContinuityRequired; no request is sent. ALLOW_RESET permits a deliberate rebase, preferably outside an open tool round, recording discarded block IDs, reason, and whether the round remains open. An open round must retain a valid call/result exchange after reset or fail. Deliberate resets are distinct from unexpected provider rejection/invalidation, which is always a compatibility failure. An allowed fresh conversation is not an in-place REWRITE of bound history. In autonomous loops nearly every assembly has an open tool round, so a REQUIRE profile can relieve pressure only through a verified native checkpoint (FR-MAT-005) or fail; ALLOW_RESET is the expected setting for autonomous tasks, and the harness should checkpoint (FR-TOOL-004) before a reset it can foresee.

### Cost model

- FR-COST-001: Effective task cost includes uncached input, cache writes/reads, output/reasoning, retrieval, compaction iterations, and runtime overhead without double counting. Provider prices come from the descriptor; local storage/compute/retrieval conversion rates come from a versioned cost policy. Report provider currency charges and local resource measurements separately as well as their policy-priced total. Unknown usage is labeled unknown, never zero.
- FR-COST-002: Strategy selection is a forecast heuristic subject to mandatory coverage, visibility, safe edits, budgets, configured reasoning continuity, and the semantic plan's ranking/dependencies. It cannot drop required content merely because a smaller request is cheaper. Latency, pruning regret, and cache disruption affect choices only through explicit constraints. Start with a fixed verified strategy; enable estimated-cost selection only after the baseline and forecast error have been measured.
- FR-COST-003: The forecast prices K future calls using recorded assumptions for appended bytes/tokens, output/reasoning work, retrieval probability, compaction size/cost, local overhead, and elapsed time between calls. Cache observations include last acknowledged prefix, cache age/TTL, relevant configuration, and measured hits/writes. Price both warm and cold/expired-cache cases; do not assume one write followed by guaranteed reads. K, growth assumptions, reset penalties, uncertainty bounds, and a minimum savings margin are versioned in ADR 15. Choose a discretionary rebase only when it clears the margin under the configured conservative forecast; ties or insufficient data retain the fixed baseline. Required security/budget actions override cost.
- FR-COST-004: Every materialization records forecast inputs, model/policy versions, cost components, assumptions, and considered alternatives. Call completion records actual usage per provider iteration and local measurements, allowing estimate error to be measured. Replay consumes recorded timing/cache observations, never the current clock or current prices. A changed strategy's cache observations are simulated and labeled counterfactual, not borrowed from the observed strategy.

### Provider-call lifecycle and recovery

- FR-CALL-001: Plan and Assemble are previews. PrepareCall atomically checks their semantic sequence, conversation version, policy/descriptor versions, and access snapshot; freezes the request bytes/hash and manifest; reserves one outstanding operation per conversation; and stores a PREPARED record with a stable CallID. A stale preview fails with ErrVersionConflict. No database transaction remains open during token-count endpoints or provider transport. Counts and visibility are revalidated before dispatch if their inputs changed.
- FR-CALL-002: The dispatcher persists SENT immediately before transport. The result is COMPLETED, FAILED (a known failure), or UNKNOWN (acceptance/completion cannot be established). A crash in the send/acknowledgment gap is UNKNOWN even if the request may never have reached the provider. PREPARED has no transport effect; SENT/UNKNOWN cannot be automatically resent without a verified provider idempotency or reconciliation mechanism. Each transport attempt is recorded under the logical CallID. A timeout is not proof of failure.
- FR-CALL-003: RecordCallOutcome atomically records the provider response and all usage iterations, ingests completed output with stable response/event IDs, and advances the committed conversation/epoch and usage counters exactly once. Repeated identical outcomes are idempotent; conflicting outcomes fail. Incomplete streamed blocks are audit data, never executable tool calls or committed history. Only completed inference responses advance the semantic logical-call index; compaction and failed attempts do not. A response belongs to the principal/snapshot that dispatched it, even if newer semantic events arrived meanwhile.
- FR-CALL-004: UNKNOWN retains the conversation reservation until RecoverCall reconciles a provider request/idempotency ID, or an authorized harness explicitly abandons the attempt and records the uncertain usage before starting a fresh epoch. The runtime promises no exactly-once external inference or tool execution. The harness must deduplicate tool effects by tool-call ID; recovery never silently re-executes them. Known failures may retry under a recorded policy after revalidating the request. Compaction operations follow the same ledger and restart rules.
- FR-CALL-005: At most one operation is PREPARED, SENT, or UNKNOWN for a conversation. Concurrent preparation fails with ErrCallInFlight or ErrVersionConflict; it does not implicitly fork history. Semantic ingestion can continue while a provider operation runs. New requirements/visibility changes invalidate an unsent preview and apply to the next dispatch; an already sent request retains its audited snapshot. Conversation commits use compare-and-swap versions, and cross-task semantic mutations serialize per session.

RecoverCall may cancel a still-PREPARED operation as a known unsent failure, releasing its reservation. Once SENT is durable, recovery must follow FR-CALL-004. Reconciliation that finds a completed provider response uses RecordCallOutcome; explicit abandonment is terminal for that attempt, and a late response is audited without silently attaching it to a newer epoch.

### Rendering

- FR-RND-001: Mandatory status decides inclusion, not role. At a rebase, render trusted SYSTEM/HARNESS policy in the descriptor's privileged slots, then memory/current-state blocks at their original authority in deterministic category/sequence order, then the conversation's newest checkpoint as assistant content, then selected logical conversation exchanges ending with pending input. USER goals and pins remain user content; agent knowledge remains assistant content; historical evidence remains labeled data. An item repeated for current-state presentation is identified by ID/version and deduplicated where the protocol permits. The manifest accounts for every actual representation. Provider structure takes precedence over cosmetic section ordering.
- FR-RND-002: Render SYSTEM at system authority and HARNESS at the provider's developer-equivalent role where supported. Where both share a native slot, keep harness text explicitly subordinate to SYSTEM policy in the trusted template. USER content is never copied into a privileged slot, including after compaction or inside compaction instructions. AGENT content is assistant content; tool evidence is a correctly paired tool result or an explicitly labeled data projection carrying its provenance; retrieved/embedded text is delimited data in a non-privileged role. Obligations and semantic deltas retain their source authority. Escape delimiter/section spoofing in untrusted content. Runtime-owned framing cannot confer instruction authority on its quoted payload.
- FR-RND-003: Every rendered request satisfies the target provider's structural rules, including message ordering, tool call and result pairing, and reasoning placement. Assembly fails with ErrInvalidProviderRequest rather than return an invalid request.
- FR-RND-004: At a rebase, items in the plan's retrievable set and exchange groups covered by the checkpoint may render as stubs (ID, kind, short description, token size) within a policy stub budget, so the model can request them (FR-RET-006).

### Garbage collection, retrieval, and persistence

GC operates on semantic state. It decides whether evidence is still active, whether state is obsolete or superseded, whether an item can archive or be retrieved later, and whether it is mandatory. It does not decide what bytes the next request carries (FR-MAT-004).

- FR-GC-001: GC runs on resident SemanticBytes above configured task/session limits, task completion, supersession, TTL expiration, explicit lifecycle command, or policy request. These limits are independent of model windows. Protected current requirements cannot be collected to meet a size target; an unreclaimable excess is reported with protected-byte counts and requires an authorized lifecycle change. Provider input overflow remains a separate assembly error.
- FR-GC-002: GC archives by default. Automatic deletion is outside V1.
- FR-GC-003: Current pinned requirements and OPEN goals remain protected from automatic archival while their owning scope is active. Authorized replacement/resolution or the declared scope ending releases that protection; ordinary pressure never does. Superseded historical versions may archive without losing content, directive IDs, status history, or provenance. A retrieval lease cannot recreate a historical pin's protection.
- FR-GC-004: Stale, duplicated, superseded, and unused ephemeral items are preferentially collectible.
- FR-GC-005: GC is transactional and idempotent.
- FR-GC-006: GC changes reach the rendered request only through the strategy (FR-MAT-004).
- FR-RET-001: Search, Get, and Rehydrate enforce the immutable access boundary. Search/Get may return authorized expired, resolved, completed-task, or superseded evidence with its historical status; automatic planning has the stricter eligibility rules in FR-DOM-003. Rehydrate explicitly issues a principal/task/agent/turn-bound retrieval lease. It cannot widen ownership or restore a requirement.
- FR-RET-002: V1 search works without a vector database through a deterministic lexical/indexed implementation.
- FR-RET-003: Rehydration records the principal, triggering actor, query, item IDs, latency, success/failure, and assembly usage.
- FR-RET-004: Successful rehydration followed by model consumption contributes to a capped recurrence signal. Count an item once per completed logical inference in a fixed policy window of logical calls, independent of provider strategy or epochs. Duplicate retries and repeated tool reads within one call add no credit. Actual elapsed retrieval latency is reported but never enters semantic recurrence scoring.
- FR-RET-005: An EvictionEpisode begins when previously available evidence becomes unavailable to the model through omission, archival that removes its last representation, or compaction that loses direct recoverability. Record item ID, task, logical call index, prior representation, cause, and whether it remains directly present in history. Repeated exclusions and intervening rebases do not restart the episode. Successful recovery closes it and records calls/elapsed time to recover; failed/slow attempts remain attached. First-use retrieval is distinct from recovery. Report recovery within a fixed policy call window plus censored/unrecovered episodes at task end. Native compaction with opaque, unverifiable coverage is labeled unknown and reported separately, never scored as zero regret.
- FR-RET-006: Provider-neutral archive tools return authorized item content as labeled historical evidence with ID, status, and provenance, through a valid tool exchange. Retrieval leaves persisted Residency unchanged; GoalStatus, generation, currentness, and ownership likewise remain unchanged. A holder-bound lease, not a residency change, supplies temporary historical-evidence admission: the lease binds immutable source occurrence/content, while the retrieval result records observed lifecycle revision/status, and protects the fetched content from automatic eviction for bounded logical calls in its requesting turn. Oversized retrievals use the same explicit delivery projection as FR-ASM-012. Expired leases never keep content in another principal's request.
- FR-PER-001: V1 provides in-memory and SQLite stores; Postgres may implement the same interface after SQLite acceptance.
- FR-PER-002: Persistence covers immutable items/blobs and hashes, relationships, authority/access boundaries, generations, residency and goal status, directive/obligation versions and grants, applicability fingerprints, lifecycle events, retrieval leases, eviction episodes, task/turn state, epochs and source manifests, call/attempt ledger, request bytes/hashes, complete provider responses including opaque blocks, decision records, versioned token caches, usage iterations, forecast inputs, and cache/timing observations.
- FR-PER-003: Restart reconstructs the same logical state and relationships.
- FR-PER-004: Mutations use transaction protection and preserve graph integrity under concurrent ingestion.

### Providers

- FR-PROV-001: Core uses a normalized provider interface for messages, responses, and usage metadata.
- FR-PROV-002: V1 includes deterministic fake, OpenAI-compatible, and Anthropic-compatible adapters. The OpenAI-compatible adapter also serves local models behind OpenAI-compatible servers. Adapters are stateless: they send the full rendered request and never rely on provider-side conversation state. ADR 9 fixes the OpenAI API surface.
- FR-PROV-003: Strategies and adapters cannot change semantic state, and adapters cannot change strategy decisions. Provider-native context management is used only through a strategy the descriptor supports (FR-MAT-003), and each use is recorded on the assembly.
- FR-PROV-004: Each adapter provides a serializer, capability descriptors, and counters for its models. The default counter is a local estimator whose per-model calibration is updated from provider-reported input tokens (FR-COST-004) and whose declared error bound sets the safety margin; an exact counter backed by a provider count endpoint, where one exists, serves verification under NFR-003 and never runs on the ingestion path. Serializers and counters implement interfaces defined in internal/domain, so strategies do not import adapters.
- FR-PROV-005: Adapters preserve canonical assistant content and native reasoning/compaction blocks, including opaque fields and unknown compatible block types, with their order and provider binding. RecordCallOutcome stores them as provider conversation state plus audit references. Replay follows FR-ASM-011; a provider block is never reinterpreted as a trusted semantic directive or portable summary.
- FR-PROV-006: Adapters report provider usage, including cache-read and cache-write input tokens and reasoning drops where the provider reports them.

### Observability, replay, and simulation

- FR-OBS-001: Emit structured events for ingestion, parsing, relationships, transitions, GC, retrieval, rehydration, planning, strategy selection, epochs and rebases, native operations, provider calls, obligations, and errors.
- FR-OBS-002: Report correctness signals (constraint violations, lost goals, lost obligations), context size (raw transcript tokens, active semantic memory, provider-visible tokens, peak context), cache behavior (cached, uncached, cache-write, and cache-read tokens; hit ratio; invalidation events; stable-prefix length), cost (input, output, and cache cost; cost per task and per successful task), memory management (archives, rehydrations, repeated rehydrations, supersessions, promotions, demotions), reliability (pruning regret, rehydration failures, provider compatibility errors such as rejections or reasoning drops caused by runtime edits, assembly failures, counter error), and latency (planning, retrieval, materialization, provider, and total task).
- FR-OBS-003: Sessions record ordered semantic events and immutable provider-operation records, input/output snapshots, full manifests, observed timing/cache data, and expected final state. Exact replay uses recorded provider outputs and count-endpoint results with the original policy, descriptor, counter/serializer versions, and budget. This reconstructs the executed history; it does not regenerate opaque blocks for an alternative history.
- FR-OBS-004: Identical semantic inputs produce identical plans. Identical plans, conversation snapshots, principal/eligibility snapshots, budgets, policies, descriptors, counter results, and forecast observations produce identical decisions and rendered output. Cross-strategy replay compares semantic behavior and simulated resource use; it cannot assert reasoning validity by replaying opaque blocks against a different prefix.
- FR-OBS-005: Offline replay runs a recorded session through each strategy with the fake provider and the simulator, holding recorded model responses fixed. Live evaluation runs the benchmark against real models for each mode (section 12).
- FR-SIM-001: The simulator takes a recorded session, timing schedule, descriptor, and forecast policy and estimates cache use, writes, compactions, context pressure, and cost per strategy. It holds model responses fixed and labels retrievals as recorded workload or explicit forecasts; it cannot establish the alternative agent's tool choices or correctness. Cold/warm caches, TTL expiry, and alternative output/compaction assumptions are separate deterministic scenarios.
- FR-SIM-002: The simulator models provider-native compaction with a policy-configured size model; it does not predict compaction content.
- FR-SIM-003: Live runs periodically validate the simulator, and the difference between simulated and actual cost and cache usage is reported.

## 4. Non-functional requirements

Before measuring the Phase 5 vertical slice, ADR 10 fixes a reproducible performance profile: machine/CPU/RAM, OS and Go/SQLite versions, database size and indexes, storage mode, concurrency, content-size distribution, and warm/cold cache conditions. The initial primary profile has one preparing conversation, 10,000 task candidates, and 1 KiB median/8 KiB p95 text items. Report large-blob and concurrent-session stress results separately. Targets cannot be changed after results without an explicit spec revision.

- NFR-001: Assemble p95 latency on the primary warm-cache SQLite profile is at most 100 ms, excluding remote counting/compaction. Report those remote durations and total wall latency separately; skipping them does not establish end-to-end latency.
- NFR-002: Ingest p95 latency is at most 20 ms for already acquired immutable events in the primary profile. Report acquisition time and blob throughput separately.
- NFR-003: Token cache keys follow FR-ING-007. Whole-request remote counts are reused only for identical count inputs. The baseline allows one whole-request count per attempt, plus bounded recounts after changed projection or native compaction; every recount is measured. Do not trade budget correctness for a remote-call limit.
- NFR-004: The benchmark reports store growth per model call, including assembly records and decision traces.

## 5. Hard invariants

- INV-01: Every submitted inference or compaction operation fits its own usable input budget before new server transformations, under a verified counter bound. Model-visible and billed-iteration counts are separately reported.
- INV-02: Mandatory context never disappears silently.
- INV-03: Current pins and OPEN goals remain protected while their declared owning scope is active. Only an authorized lifecycle change, replacement, or declared scope end releases protection; historical versions remain auditable.
- INV-04: Lower-authority content cannot promote itself or override higher-authority context, including through supersession, deduplication, or obligation transitions.
- INV-05: No API or transmitted provider state crosses the principal's access boundary. Plans and model-visible context additionally satisfy temporal scope/TTL eligibility or an explicit retrieval lease, including inherited history and opaque source coverage. Residency affects new selection; authorized already rendered history follows FR-MAT-004. Historical reads cannot reactivate requirements.
- INV-06: Supersession graphs are acyclic.
- INV-07: Required derived items retain valid provenance.
- INV-08: Archived snapshots remain intact, auditable, and retrievable by authorized principals without changing semantic status or currentness.
- INV-09: Stable input identities and the complete recorded inputs in FR-OBS-004 yield deterministic replay. Retried event/call outcomes apply side effects once.
- INV-10: Restart and concurrent mutation preserve valid state and relationships.
- INV-11: Every rendered request is structurally valid for its target provider.
- INV-12: Within an epoch, each rendered request is the previous one plus appended content, changed only by SAFE edits. A LOSSY edit is a recorded deliberate reset that starts a new epoch.
- INV-13: No request replays reasoning items outside the epoch that produced them, no strategy performs a REJECTED edit, and no strategy requests a native operation the target does not declare.
- INV-14: Every materialized requirement retains its source authority; mandatory inclusion and compaction never promote a USER item into privileged content.
- INV-15: Epochs advance only on an atomic recorded completion. An unknown transport outcome cannot cause an implicit resend, conversation fork, or replayed tool effect.
- INV-16: A current obligation can be SATISFIED only by proof applicable to the declared current resource state or an explicit authorized assertion.

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
    internal/invocation   call ledger, preparation, outcome recording, recovery and conversation reservations
    internal/tools        agent-facing tool definitions and handlers: semantic writes, checkpoint, retrieval
    internal/retrieve     archive search and rehydration
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
        TurnID       string
        Access       AccessBoundary
        Residency    Residency         // RESIDENT or ARCHIVED
        GoalStatus   *GoalStatus       // OPEN or RESOLVED; goals only
        Retention    RetentionClass    // PROTECTED, HIGH, NORMAL, LOW
        Parts        []ContentPart     // text, plus image and document references
        ContentHash  string
        SemanticBytes uint64           // canonical content/blob size, provider-independent
        Tokens       map[CounterID]int // derived cache, excluded from semantic policy
        Importance   int64             // fixed-point
        CreatedAt    time.Time         // audit; never used for semantic scoring
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
        Checkpoint  *ItemRef           // the conversation's newest checkpoint
        Covered     []ItemRef          // exchange groups the checkpoint covers
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
        Edits                    map[EditKind]EditSafety // SAFE, LOSSY, REJECTED
        NativeCompaction         bool
        CompactionInstructions   bool
        CompactionProtocol       CompactionProtocol // checkpoint/pause/verified automatic
        CompactionMinimumTrigger int
        MandatoryPreservation    PreservationRules
        ContextEditing           bool
        MidConversationSystem    bool
        NativeMemory             bool
    }

    type MaterializationStrategy interface {
        Materialize(ctx context.Context, req MaterializeRequest) (MaterializationPlan, error)
    }

MaterializeRequest carries the plan, capabilities, immutable conversation and eligibility snapshots, budget/reservations, continuity mode, semantic policy, and forecast inputs. Mandatory, superseded, and rehydrated status are derived (FR-DOM-005, FR-DOM-007). Item-to-item edges live only in Relationship records (FR-REL-001); SATISFIES is a typed read derived from ObligationTransition/ApplicabilityProof records, not a Relationship row.

Required companion records are Relationship, ObligationVersion, ObligationTransition, MutationGrant, ApplicabilityFingerprint, TaskState, LifecycleEvent, Epoch, ConversationManifest, CallRecord, CallAttempt, ProviderBlock, AssemblyRecord, AssemblyDecision, ProviderDecision, RetrievalEvent, RetrievalLease, EvictionEpisode, ProviderUsage, ForecastInputs, and CostEstimate.

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
        PrepareCall(ctx context.Context, p Principal, req PrepareCallRequest) (CallRecord, error)
        MarkCallSent(ctx context.Context, p Principal, req DispatchRequest) (CallAttempt, error)
        RecordCallOutcome(ctx context.Context, p Principal, req CallOutcome) (CallRecord, error)
        RecoverCall(ctx context.Context, p Principal, req RecoveryRequest) (CallRecord, error)
        Search(ctx context.Context, p Principal, req SearchRequest) ([]SearchHit, error)
        Get(ctx context.Context, p Principal, id string) (ContextItem, error)
        Rehydrate(ctx context.Context, p Principal, req RehydrateRequest) (ContextItem, error)
        Collect(ctx context.Context, p Principal, req CollectRequest) (CollectReport, error)
        ExplainAssembly(ctx context.Context, p Principal, assemblyID string) (AssemblyRecord, error)
        Provenance(ctx context.Context, p Principal, id string) (ProvenanceGraph, error)
        TransitionObligation(ctx context.Context, p Principal, req ObligationTransition) (Obligation, error)
        Tools(ctx context.Context, p Principal) ([]ToolDefinition, ToolHandler, error)
        Inspect(ctx context.Context, p Principal, req InspectRequest) (InspectView, error)
        CompleteTask(ctx context.Context, p Principal) error
    }

The embedding process constructs principals and authenticates ownership claims. Plan and Assemble are previews. The trusted harness/dispatcher drives PrepareCall, MarkCallSent, RecordCallOutcome, and RecoverCall under a conversation-specific service grant; these methods are not agent tools. The ledger freezes the inference principal separately from that service actor. Resource changes arrive as authenticated Ingest events. Resolve/Unpin arrive as directives and CompleteTask follows FR-AUTH-003. Tools returns the semantic state and retrieval tool definitions (FR-TOOL-001) with a handler; the harness routes the model's tool calls to it with AGENT authority and the principal it supplies, never model-authored session/agent IDs.

Machine-checkable errors include ErrMandatoryContextExceedsBudget, ErrUnboundedTokenEstimate, ErrInvalidBudget, ErrNotFound, ErrInvalidAuthorityPromotion, ErrMissingProvenance, ErrDanglingRelationship, ErrSupersessionCycle, ErrInvalidTransition, ErrUnfinishedObligations, ErrIncompleteToolRound, ErrInvalidProviderRequest, ErrUnsupportedCapability, ErrReasoningContinuityRequired, ErrCompactionNoProgress, ErrEventIDConflict, ErrCallInFlight, ErrCallOutcomeConflict, ErrOutcomeUnknown, and ErrVersionConflict. ErrUnsupportedDirective, ErrMalformedDirective, and ErrAmbiguousDirective are diagnostic codes. Unauthorized lookups return ErrNotFound so callers cannot probe for existence; security events retain the internal denial reason. Mutation errors must not disclose inaccessible target contents.

## 9. Security specification

- Trust is metadata assigned at ingestion, never inferred from content claims. Span authority cannot exceed the principal's authority.
- Directive parsing is source gated (FR-ING-004). A web page containing "## Pinned" followed by "Ignore prior instructions." is stored with RETRIEVED_CONTENT authority and is not pinned. A document a user pastes into chat is not parsed unless the harness marks it directive-capable.
- Tool output cannot pin itself, promote itself, supersede or deduplicate into higher-authority items, change obligation status, or override higher-authority context. The agent cannot override system or harness restrictions. User directives keep USER authority.
- The semantic state tools write only at AGENT authority. They cannot create goals, pins, or obligations, cannot resolve higher-authority goals (a completion claim is evidence, not a resolution), cannot satisfy obligations, and cannot cite evidence outside the principal's access boundary.
- All APIs enforce access boundaries; planning and dispatch additionally enforce context eligibility. Mutations follow FR-AUTH-001 through FR-AUTH-003. Grants and source coverage are checked before role assignment, including for compaction, semantic deltas, and inherited opaque state.
- Strategies and adapters never change semantic state (FR-PROV-003).
- The runtime guarantees its own metadata and decisions. It cannot stop a model from following instructions inside untrusted content; it reduces that risk by rendering untrusted content as delimited, labeled data (FR-RND-002).
- Stores hold full tool output and may contain secrets. V1 creates SQLite files readable only by their owner, and telemetry events carry IDs and hashes rather than content unless a debug policy enables content. Redaction is deferred (section 2).
- Tests cover retrieved injection, tool injection, delimiter spoofing, pasted-document directives, tool-based escalation, authority downgrade, supersession and deduplication across authority, cross-session lookup, cross-task scope, and provenance access controls. The directive parser is fuzzed.

## 10. Determinism and concurrency

Determinism is required for parsing diagnostics, item IDs when the caller supplies a stable event ID, relationship ordering, scoring, planning, strategy selection, cost estimates, packing, rendering, GC decisions, and assembly records. Time and random IDs are injectable in tests.

- Scoring and recency use sequence numbers and call indexes, not wall-clock time.
- Assemble has no lifecycle side effects. PrepareCall records a frozen proposal; RecordCallOutcome atomically advances conversation state and applicable usage counters. Retrieval, resource-change, and lifecycle events are recorded in commit order. Replay reapplies each stable event/outcome once.
- Scores and cost estimates are fixed-point integers. Go permits fusing floating-point multiply-add, and the gc compiler does so on arm64 but not on default amd64 builds, so floating-point results could differ between an Apple Silicon laptop and an amd64 CI runner.
- Sequence numbers are assigned per session at commit and define the order replay uses.
- Cache forecasts use persisted timing/usage observations and versioned assumptions; elapsed time does not affect semantic scoring or plan construction. Retry attempt timing is distinct from completed logical inference indexes.

Mutations serialize per session because WORKFLOW, SESSION, and AGENT items can be shared across tasks; transaction retries must preserve event idempotency. Reads use consistent snapshots, and call preparation/dispatch revalidate their versions. Conversation reservations cover transport without holding database transactions open. Tests cover concurrent duplicate ingestion, supersession, GC, conflicting call preparation, partial streaming, and crash recovery before/after each durable call transition under the race detector.

## 11. Delivery phases and exit gates

Each phase lists the ADRs (section 15) that must be accepted before it exits.

1. Contracts, domain, and stores: accept authority/access/residency and call-ledger decisions before schema implementation; add memory and SQLite stores, immutable blobs, idempotency, transactions, and recovery records. ADRs 1, 3, 4, 6, 13, 16, 17. Gate: state-transition, restart, graph, and concurrency tests for the foundational event traces. A throwaway live probe of the Claude and OpenAI descriptor assumptions (reasoning binding, cache reads, compaction protocol) runs during this phase so ADR 12 rests on observed behavior before the vertical slice.
2. Directives and ingestion: grammar, IDs, span boundaries, immutable snapshots, deterministic classification, and replacement. ADR 19. Gate: canonical directive examples, parser fuzzing, retry identity, and injection resistance.
3. Semantic state engine: current versions, goal status, Working snapshots, keyed agent writes, checkpoints and coverage, observation identity, evidence applicability, obligation transitions, and basic archival/leased retrieval. ADR 8. Gate: replacement, resolution/rehydration, evidence invalidation, and mutation-authority traces.
4. Context planner: access/eligibility, mandatory partition, provider-independent sizes/scores, plans, and semantic decisions. ADRs 5, 7. Gate: identical plans across provider counters and planner import check.
5. First vertical slice: fake adapter and one real provider with fixed Rebuild/AppendOnlyCached modes, role-preserving rendering, verified counters/descriptors, tool exchanges including the semantic state tools and reference instruction block, call ledger, checkpoint compaction where supported, and restart recovery. The fake adapter enforces its descriptor (edit safety, tool pairing, cache-prefix accounting, compaction protocol) and is configurable to model rewrite-safe and reasoning-bound profiles. ADRs 2, 9, 10 (initial fixture/profile), 11, 12, 18. Gate: actual requests satisfy authority, scope, tool pairing, budget, reasoning, compaction, and crash traces before building an optimizer.
6. Archive and retention: indexed search, retrieval tools/projections, leases, GC pressure, recurrence, eviction episodes, and full explanations. Gate: evidence recovery across rebases and complete inherited membership records.
7. Provider coverage: complete OpenAI-compatible and Anthropic-compatible adapters and supported local profiles; verify native protocols and deliberate-reset policies. Gate: supported-profile fixture/live tests and no unsupported native operation in application traffic.
8. Baseline measurement: record the coding workload in Modes A, B, C where available, and D with a fixed strategy. Finalize ADR 10 run count, statistical method, resource limits, and correctness oracle before comparative runs. Gate: usable correctness/cost/cache data and independently verified semantic invariants.
9. Estimated-cost policy and simulator: add forecasts, expiry-aware cache scenarios, rebase margins, and policy-selected strategies. ADR 15. Gate: deterministic replay, measured forecast error, and comparison against the fixed strategy; missing observations retain the baseline.
10. End-to-end evaluation: run Modes A through D plus the fixed-strategy diagnostic under the predeclared protocol. Gate: comparative evidence, uncertainty intervals, regret episodes, and cost per success; simulations are not correctness evidence.
11. Hardening and release: security, stress, performance, archive integrity, concurrency, and restart tests on the supported profiles. ADR 14. Gate: all hard invariants and NFR targets pass and comparative outcomes satisfy section 12. A missing target requires an explicit spec/scope revision, not a silent waiver.

## 12. Test specification

Test classes: unit, property and invariant, fuzz, integration, provider compatibility, cache simulation, replay, concurrency, restart durability, security, and end-to-end.

### Unit tests

Cover directive grammar and retry identity; source authority in every rendering path; access versus eligibility; residency versus goal status; current-version replacement; observation comparability; evidence applicability; authorized obligation transitions; provider-independent scores; request budgets and delivery projections; call-state transitions; checkpoint compaction; cache forecasts; immutable snapshots; and eviction-episode accounting. Each normative event trace names the requirements its tests must cover.

### Required behavior tests

- Semantic: current eligible requirements never disappear; authorized replacement retires old versions; resolved goals stay resolved through archival/retrieval; ephemeral evidence can archive; evidence freshness invalidates satisfaction; provenance remains intact; a keyed agent write supersedes its predecessor; a Working snapshot supersedes the previous snapshot; the newest checkpoint renders at a rebase and its covered groups are excluded by default; the turn-opening message is always present.
- Authority: retrieved content cannot pin itself; tool output cannot elevate authority; the agent cannot override system or harness restrictions; user directives keep user authority; the semantic state tools cannot create goals, pins, or obligations, resolve higher-authority goals, or cite inaccessible evidence; pasted documents are not parsed without harness marking.
- Provider: appends preserve permitted history, user requirements keep user roles, every transmitted operation fits budget, native compaction checkpoints restore requirements before task inference, REQUIRE rejects a reasoning reset, and ALLOW_RESET records a deliberate reset without claiming accidental preservation; REJECTED edits are never performed and LOSSY edits appear only as recorded deliberate resets.
- Cache: stable-prefix preservation is measurable; equivalent semantic state does not invalidate the cache; the cost model recognizes when a rebase costs more than cached continuation.
- Isolation: session/agent boundaries apply to the complete request and opaque coverage; new turns remove expired automatic context; explicit historical retrieval remains authorized and lease-bound.
- Recovery: repeated events/outcomes are idempotent, conflicting retries fail, concurrent preparations do not fork a conversation, and UNKNOWN outcomes are not automatically resent.
- Metrics: identical eviction/recovery sequences receive identical recurrence/regret attribution despite different rebase schedules; recorded elapsed times affect cache simulation only.

### Property tests

Property tests exercise INV-01 through INV-16 over generated histories, including authority/scope changes and crash points. They must check full rendered requests and inherited manifests, not just the selected candidate list. Protection properties apply to current requirements within their declared active scope; resolved and superseded historical versions must not become mandatory through retrieval. Identical recorded inputs must replay identically across architectures and cache warmness.

### Fuzz tests

Fuzz the directive parser and the renderer's escaping with Go native fuzzing.

### Integration tests

Prove SQLite restart/migration integrity, immutable blob availability, event/call idempotency, scope and authority preservation through every rendering mode, archive/retrieval leases, evidence invalidation, and adapter compatibility with golden requests and recorded responses. Inject crashes before prepare, before/after durable SENT, during streaming, before outcome commit, and after commit before acknowledgment. Live descriptor probes verify real behavior; fixture playback alone cannot establish provider support. Run concurrent paths under the race detector.

### End-to-end benchmark

Use a deterministic repository/tool fixture for dependency migration: read architecture requirements, inspect source, run tests, hit failures, inspect docs/changelog, modify code, rerun tests, and produce a final result. Raw history must exceed the configured inference window by at least 4x. Include noisy logs, a requirement replacement, a source change after passing tests, and evidence retrieval after multiple rebases. Include an individually oversized tool result to exercise the common delivery-projection policy. The fixture and correctness oracle are deterministic; live model decisions are not assumed deterministic.

Run the workload in four modes:

- Mode A, raw transcript: the traditional growing history. When it exceeds the context window, the oldest exchange groups are dropped and each drop is recorded.
- Mode B, minimal rebuild: the runtime with the Rebuild strategy.
- Mode C, provider native: the provider's caching, compaction, and context editing, without the semantic runtime. The agent runs the same prompt as Mode D minus the reference instruction block and the semantic state tools, so the runtime's tools and representation are the only intervention.
- Mode D, Context Runtime: the semantic runtime with the cost-selected provider-aware strategy, the reference instruction block, and the semantic state tools.

The working hypothesis is that semantic requirement/evidence management improves outcomes while a provider-aware strategy controls cost. Add a fixed-strategy Mode D diagnostic so the optimizer's benefit is measured independently. If native features are unavailable, mark Mode C unavailable for that profile; do not substitute Mode A and claim a native comparison. Offline replay establishes deterministic policy/resource behavior; only independent live runs establish task correctness.

ADR 10 freezes model snapshot and inference settings, harness/tools and source metadata, initial repository bytes, workload variants/seeds, immutable tool-output projection policy, output/tool/time/currency limits, common retry policy, and provider-native options before comparisons. Run modes in randomized/interleaved paired fixture blocks and report warm/cold-cache strata, sample count, failures, and 95% intervals. Harness-generated semantic assertions must not use an oracle unavailable to the baseline agent. All modes receive the same source requirements; runtime-specific representations are the intervention.

Predeclare the correctness non-inferiority margin and power/run-count method, and a confidence-interval method for cost ratios. Report the margin alongside the result; it is a statistical tolerance, not proof of exact equality. A point estimate alone does not establish acceptance, and an inconclusive comparison is reported as inconclusive. Include all failed/retried attempts in cost per attempted task and total cohort cost divided by successful tasks; zero successes makes cost per success undefined, never zero. Unknown usage and missing outcomes must be accounted for before an economic claim.

Success criteria for Mode D:

- Correctness point estimate no lower than Modes A and C where available, with the predeclared non-inferiority test passed. Report constraint retention and recovery correctness separately from final test-suite success.
- Effective cost per attempted task and per successful task no higher than Modes A and C where available, supported by the predeclared cost-ratio interval test. Report measured provider charges, policy-priced local overhead, and forecast error separately.
- Zero lost current eligible pins/goals, zero cross-session leakage, zero invalid provider requests, zero submitted-input budget violations, and zero provenance corruption. Compare count bounds against provider-reported input where measurements refer to the same processing stage; effective post-compaction counts cannot certify pre-compaction submission size.
- Zero unsafe history edits and unexpected reasoning rejections/drops caused by runtime edits. Deliberate ALLOW_RESET transitions are reported separately with their rate, discarded blocks, cost, and correctness outcomes; REQUIRE profiles must have zero such resets.
- Bounded semantic working memory, recoverable archived evidence, and inspectable decisions.

Token reduction is reported for every mode but is not a success criterion. A profile failing a hard invariant cannot pass on cost savings. Comparative failures or inconclusive results require more evidence or an explicit revision of the release claim; targets cannot be silently relaxed.

## 13. Inspection contract

Inspect exposes a text or structured view, similar to a /context command in coding harnesses, with the active goal, pinned items and obligation statuses, working items, durable items, the newest checkpoint, archived counts, token usage, the current strategy and epoch, recent GC and provider decisions, cache statistics, and unresolved requirements. Inspection is read-only and uses runtime authorization and scope checks.

## 14. Acceptance checklist

V1 is accepted only when:

- Authorized directives are parsed per the grammar and persisted with source metadata.
- The agent records facts, decisions, state, and checkpoints through the semantic state tools at AGENT authority; keyed writes supersede and cannot escalate.
- Current goals, constraints, and obligation versions are maintained; Resolve, Unpin, replacement, and CompleteTask obey the common authorization matrix.
- Current state is tracked independently from transcript history, with supersession and provenance queryable through Get, Search, and Provenance.
- Current requirements remain protected within their active scopes; historical retrieval never recreates mandatory status.
- Authority boundaries hold: tool and retrieved content cannot create privileged directives or override higher-authority items through supersession, deduplication, or obligations.
- Unresolved obligations are visible; satisfaction is backed by applicable evidence and invalidates on relevant resource changes before another test run.
- Archived evidence is searchable and rehydratable by the harness and through the retrieval tools.
- Context Plans are provider-independent and deterministic.
- Provider capabilities are modeled as versioned descriptors, verified by adapter contract tests.
- The runtime selects among Rebuild, AppendOnlyCached, and ProviderNative strategies, preserves append-only history when the descriptor requires it, and rebuilds when the cost model favors it.
- Verified provider-native checkpoints preserve source authority and restore the entire mandatory set before task inference resumes.
- Mandatory context never silently disappears, the token budget is never exceeded, and every rendered request is valid for its provider.
- Cache behavior, estimated and actual cost, and pruning regret are tracked.
- Every semantic and provider decision is explained.
- Session and task isolation is enforced.
- Retry idempotency, the durable call ledger, UNKNOWN recovery, and conversation reservations preserve valid state through restart and concurrent operations.
- Offline replay runs recorded sessions across strategies, and the simulator is validated against live runs.
- The end-to-end workload completes, and Mode D meets the success criteria in section 12.
- Unit, integration, property, fuzz, security, concurrency, replay, and end-to-end tests pass, including every [normative event trace](docs/sdd-event-traces.md).

## 15. ADRs required before affected phase exits

Section 11 maps each ADR to the phase it gates.

1. Go module path and minimum Go version.
2. Strict submitted-input accounting per provider operation, reservation semantics, verified counter bounds, safety margins, and handling of unsupported profiles.
3. SQLite driver and migration mechanism.
4. Stable IDs, event/call idempotency keys, conflict detection, and canonical hashes.
5. Semantic scoring weights, SemanticBytes encoding, fixed-point scale, relevance threshold, resident-byte limits, soft-pressure fraction, retrieval-call windows, stub budget, checkpoint size limit, and decision-trace retention.
6. Access-boundary and context-eligibility matrix, historical leases, expiry, and epoch validation (confirms FR-DOM-003).
7. Lexical index and normalization rules.
8. Observation identities, obligation matcher/claim versions, evidence applicability fingerprints, mutation grants, and invalidation rules.
9. Provider transport libraries, retry policy, the OpenAI API surface, and the verified reasoning replay rules for OpenAI reasoning models.
10. Benchmark fixture/oracle, comparative statistics and run counts, available baseline profiles, shared resource limits/projections, and reproducible performance hardware/data profile.
11. Render templates and delimiters per provider.
12. Capability profiles/verification, three-valued edit safety per profile, canonical provider blocks and source coverage, REQUIRE/ALLOW_RESET policies, and checkpoint compaction protocols.
13. Deployment model: embedded library, sidecar process, or both.
14. Content redaction and retention after V1.
15. Forecast horizon/growth/output/compaction assumptions, recorded cache/timing inputs, uncertainty/savings margins, local resource pricing, and simulator validation limits.
16. Common mutation authorization, directive/obligation version replacement, independent residency/goal status, and immutable source snapshots.
17. Provider call/attempt state machine, conversation reservation, outcome reconciliation, streaming completion, and crash/retry tests.
18. Semantic state tool schemas, result formats, and the reference instruction block.
19. Directive parsing and ingestion (grammar deviations, parse units, ingestion pipeline, receipts, dedup/replacement/snapshots, lifecycle parse/authorize).

Each ADR records the decision, alternatives, compatibility impact, and tests that lock the behavior.

## Appendix A: Development tooling (non-normative)

Use the current flagship reasoning model, GPT-6 Astra, with high reasoning effort for architecture, implementation, debugging, and long-horizon coding. Use extra-high reasoning selectively for security review, replay correctness, and final hardening when evaluation shows a measurable benefit. Use the balanced or efficient model tiers for routine mechanical work after the contract and tests exist.

This workload needs judgment across a large design surface, tool use, deterministic implementation, and failure analysis. Current official OpenAI guidance recommends Astra for ambitious deliverables and complex reasoning, recommends high reasoning for complex agentic workflows, and recommends extra-high for long-running agentic tasks. This guidance will age and does not constrain the runtime, which remains provider independent.

Sources: [OpenAI model selection](https://developers.openai.com/api/docs/guides/model-selection), [OpenAI reasoning models](https://developers.openai.com/api/docs/guides/reasoning).
