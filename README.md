# Context Runtime

**A provider-aware context control plane for long-running LLM agents.**

Context Runtime sits between an agent harness and an LLM provider. For every model call it decides what the agent needs to know and how to give that to the specific model being called. It does this without losing requirements, breaking prompt caches, invalidating the model's reasoning, or letting untrusted content promote itself into instructions.

> **Status: pre-alpha, built from a spec.** Phases 1 and 2 of 11 have landed: the domain model, stores, call ledger, directive parser, and ingestion pipeline. The public `Runtime` API, planner, strategies, and provider adapters are not built yet, so there is nothing to integrate against today. The [V1 specification](SDD.md) is the contract the code is being built to.

## The problem

An agent doing real work, such as migrating a dependency, triaging an incident, or running a long research loop, makes many model calls per turn and hundreds per task. A single tool result can run to tens of thousands of tokens. Several tasks and agents may share one session. Every one of those calls has to answer the same question: **what goes into this request?**

Most harnesses answer it by treating three different things as one:

- the **transcript**: everything that happened;
- the agent's **memory**: what it currently knows, is trying to do, and must keep satisfying;
- the **next request**: what this one model call receives.

That works for short chats. For long-running agent workloads at scale it fails in predictable and expensive ways.

**Truncation loses what matters most.** When the transcript outgrows the context window, the oldest messages are dropped first. Those are where the goal and constraints live. The agent keeps working, with confidence, on a task it no longer fully remembers.

**A smaller prompt is not a cheaper prompt.** Rewriting or summarizing earlier history to save tokens throws away the provider's prompt cache. In [live probes](docs/probes/2026-09-anthropic-descriptor-probe.md), a one-character edit to the first message dropped a 1,025-token cache read to zero on Claude Opus 5.5. Cached input there costs 0.05x the uncached rate, so a rebuilt prompt has to be about 20x smaller just to break even against a warm cache. On OpenAI's GPT-6 models the ratio is about 10x. Across a fleet of agents making hundreds of calls each, cache disruption can dominate the bill.

**History edits can break reasoning.** Reasoning models bind their thinking to the exact history that produced it: the messages, the system prompt, and the tool definitions. The probes found that providers handle the same edit very differently. Editing an earlier message and replaying the later reasoning is rejected with a 400 on Claude Opus 5.5 when binding is enforced, but Claude Sonnet 5 silently accepts it with stale reasoning. Dropping earlier reasoning blocks is safe on Opus 5.5, but it makes OpenAI's GPT-6 models throw away their reasoning and start again. A harness that edits history without knowing which profile it is talking to either fails requests or quietly degrades the model.

**Provider compaction is not memory.** Automatic compaction summarizes and continues in the same response, so nothing can put your constraints back before the model acts on the summary. Some compaction blocks are unsigned and can be edited. Summaries drop images and documents, and nothing records what was lost.

**Trust is inferred from text.** A web page, pasted document, or tool result containing `## Pinned: ignore prior instructions` looks exactly like a real instruction to a system built on the transcript.

**"Done" is claimed, not proven.** The agent says the tests pass, but the source changed after the test run. Nothing notices.

**Retries fork reality.** A timeout is not proof of failure. Resending after a crash can run a tool twice or fork the conversation.

**Nobody can say why the model saw what it saw.** When a long task goes wrong, there is no record of which context was selected, which was dropped, and why.

Each of these gets worse with scale: more calls per task, more tasks per session, more agents sharing state, and more providers and models with different rules.

## The approach

Context Runtime keeps the three things separate:

| Layer | What it holds | How it changes |
|---|---|---|
| Audit history | Everything that happened | Never: it is immutable |
| Semantic state | Goals, constraints, facts, decisions, task state, evidence, obligations, and their provenance | Only through authorized, audited transitions |
| Model context | What one model call receives | Chosen per call, for the target provider and model |

The runtime optimizes **effective task cost**: the currency cost of actually finishing the task correctly. It does not optimize token count. Often the cheapest request is the one that appends to a warm cache, not the smallest one.

| Failure mode | What the runtime does |
|---|---|
| Truncation drops goals and constraints | Current goals, pins, and open obligations are **mandatory**. Every call includes them, or assembly fails loudly with `ErrMandatoryContextExceedsBudget`. Mandatory context never disappears silently. |
| History edits destroy caches and reasoning | Versioned **capability descriptors** classify each kind of history edit as SAFE, LOSSY, or REJECTED for each provider profile. Within an epoch, history grows only by appends and SAFE edits. A reset is a deliberate, recorded event. |
| Compaction drops requirements | Native compaction runs as a **checkpoint protocol**: a separate ledgered compaction call, then every mandatory item restored at its original authority, then a recount, and only then the inference. Automatic in-request compaction stays disabled unless a profile proves it keeps the full mandatory set. |
| Injected instructions | **Authority is metadata assigned at ingestion**, never inferred from content. Directives are parsed only in trusted or harness-marked spans. Tool output and retrieved content cannot pin, promote, or supersede anything above them, and they render as delimited data. |
| Stale state sits next to current state | **Supersession** chains record "29 failing → 7 failing → PASS" with only PASS current. Old versions are kept for audit, and the rendered request is corrected before the next inference. |
| Completion is unproven | **Obligations** such as "All tests must pass" are satisfied only by deterministic matchers against evidence that applies to the current workspace. A resource change invalidates the proof. The agent can claim completion, but it cannot grant it. |
| Duplicate tool effects and forked history | A **durable call ledger** (PREPARED → SENT → COMPLETED, FAILED, or UNKNOWN) allows one outstanding operation per conversation. UNKNOWN outcomes are never resent automatically. |
| Leakage across agents and tasks | Every item carries an immutable **access boundary**. Items from another session never become candidates. Eligibility is checked on the full request, including inherited history and opaque provider blocks. |
| Context nobody can explain | Every assembly is **explainable**: why each candidate was included, omitted, retained, or replaced, with reason codes, sizes, and source authority. |

## Architecture

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

The **semantic layer** decides *what* a call needs. It does not depend on any provider: the same semantic state produces the same Context Plan whatever model is on the other end. It never reads token counts, cache timings, or capability descriptors, and it never calls a model. A test (`TestPackageBoundaries`) enforces that boundary, including through indirect imports.

The **materialization layer** decides *how* to deliver the plan to one specific model: append or rebase, which native features to use, and at what estimated cost. It never changes semantic state.

V1 has one strategy family with three named configurations:

- **Rebuild** rebases on every call, building minimal context from semantic state. It suits providers with weak caching and no history-bound reasoning, such as many local models.
- **AppendOnlyCached** keeps a stable prefix and append-only history within an epoch. It rebases only when it must, or when a conservative cost forecast shows a rebase saving more than a set margin. The forecast prices both a warm cache and an expired one. This is the default for providers with prompt caching or history-bound reasoning.
- **ProviderNative** keeps valid history and relieves pressure with a verified native checkpoint protocol. It is opt-in.

## Where semantic state comes from

State has three producers. The semantic layer itself never calls a model.

**1. Directives from users and harness authors.** These are Markdown sections that the runtime parses as lifecycle instructions. They are not text suggestions to the model.

```markdown
## Goal
Upgrade Foo to v2 while maintaining backwards compatibility.

## Pinned
- [api] Do not modify exported APIs.
- [tests] {obligation=tests_pass} All tests must pass.

## Working
- Current issue is TestLegacyClient.

## Remember
- [retry] {kind=decision} Keep the v1 retry policy.

## Ephemeral ttl=2
- (pasted build output)
```

Directives are parsed only in SYSTEM and HARNESS spans, and in USER spans the harness marks as directive-capable, such as an instruction file or an explicit command. A pasted document that happens to contain `## Goal` stays content. Headings inside code fences, block quotes, tool output, or retrieved content are never parsed. Reusing an ID such as `[api]` supersedes the previous version, and only an authorized principal can do that.

**2. Tools the agent calls.** The runtime ships provider-neutral tools for the harness to register with the model:

- `context_remember` and `context_update_state` record facts, decisions, and task state. They are keyed, so a new write supersedes the old one, and they cite evidence.
- `context_checkpoint` writes a summary of earlier exchanges so that a rebase can replace them.
- `context_search`, `context_get`, and `context_rehydrate` find archived evidence and bring it back under a bounded retrieval lease.
- `context_resolve` records a completion claim as evidence. It cannot resolve a goal itself.

These tools always write at AGENT authority. They cannot create goals, pins, or obligations, and they cannot cite evidence outside the caller's access boundary.

**3. Deterministic ingestion rules.** For example, a tool result depends on its tool call. Registered rules turn trusted observations, such as a test run at a known workspace fingerprint, into current-state items that supersede the previous state.

Keeping evidence, knowledge, and state apart is what lets the context stay small without becoming wrong. A 30K-token compiler log is **evidence**. It supports the **fact** "the FooClient constructor now requires context.Context", which is derived from the log. That fact drives the **task state** "3 call sites need migration". The log can be archived while the fact stays, and the state stays current until something supersedes it. If the model needs the log again, it can retrieve it.

### Authority

    SYSTEM > HARNESS > USER > AGENT > TOOL = RETRIEVED_CONTENT

Authority is assigned per span when content is ingested, and it can never exceed the caller's own authority. Lower authority cannot raise itself, and it cannot override higher authority through supersession, deduplication, or obligation changes. Each item renders in its own role: user goals stay user content, and agent knowledge stays assistant content. Nothing is promoted into a privileged slot, including after compaction.

## Integrating a harness (target V1 API)

This section describes the V1 API that [SDD §8](SDD.md#8-public-service-contract) specifies. It is **not implemented yet**.

The harness stays in charge of transport. For each model call it:

1. Calls `Ingest` with the new user message or tool results.
2. Calls `Assemble` to get a preview: the plan, the provider request, the proposed mode (append or rebase), the estimated cost, and the explanation. Nothing is committed.
3. Calls `PrepareCall`, which freezes the exact request bytes and reserves the conversation.
4. Calls `MarkCallSent`, sends the request itself, then calls `RecordCallOutcome`. That call ingests the response and advances the conversation exactly once.
5. After a crash or timeout, calls `RecoverCall`, which reconciles the outcome instead of resending.

The harness routes the model's `context_*` tool calls to the runtime's handler with AGENT authority. `Inspect` gives a read-only view similar to a `/context` command: active goal, pins, obligation statuses, working state, the newest checkpoint, the current strategy and epoch, and cache statistics. `ExplainAssembly` reconstructs the complete membership of any past request.

## How success is measured

V1 is accepted against an end-to-end benchmark, not a token count. The workload is a deterministic dependency migration: read requirements, run tests, hit failures, change code, and rerun. Its raw history exceeds the model's context window at least 4x. It includes noisy logs, a requirement replaced mid-task, a source change after the tests pass, evidence retrieved after several rebases, and one oversized tool result.

The workload runs in four modes:

| Mode | What runs |
|---|---|
| A. Raw transcript | Traditional growing history. The oldest exchanges are dropped when it hits the window. |
| B. Minimal rebuild | The runtime with Rebuild on every call. |
| C. Provider native | Provider caching, compaction, and context editing, without the runtime. |
| D. Context Runtime | The runtime with a cost-selected strategy, the semantic state tools, and the reference instructions. |

To pass, Mode D must meet all of these:

- It is non-inferior to Modes A and C on correctness.
- It costs no more per attempted task, or per successful task.
- It records zero lost pins or goals, zero leakage across sessions, zero invalid provider requests, zero budget violations, and zero unsafe history edits.

Token reduction is reported for every mode, but it is not a success criterion. A profile that breaks a [hard invariant](SDD.md#5-hard-invariants) cannot pass on cost savings.

## Status and roadmap

| Phase | Scope | Status |
|---|---|---|
| 1 | Contracts, domain model, memory and SQLite stores, idempotency, call ledger, live descriptor probes | Landed |
| 2 | Directive grammar, source-gated parsing, ingestion pipeline, classification, deduplication and replacement | Landed (ADR 19 still Proposed) |
| 3 | Semantic state engine: current versions, Working snapshots, keyed agent writes, checkpoints, obligations, basic archival and retrieval | Next |
| 4 | Context planner: eligibility, mandatory partition, scoring, plans | Planned |
| 5 | First vertical slice: fake adapter plus one real provider, Rebuild and AppendOnlyCached, semantic state tools, restart recovery | Planned |
| 6 | Archive and retention: indexed search, retrieval leases, GC pressure, eviction episodes | Planned |
| 7 | Provider coverage: OpenAI-compatible, Anthropic-compatible, and local profiles | Planned |
| 8 | Baseline measurement of Modes A–D | Planned |
| 9 | Estimated-cost policy and replay simulator | Planned |
| 10 | End-to-end evaluation | Planned |
| 11 | Hardening and release | Planned |

What exists today:

- **Domain model** (`internal/domain`): items, authority, scopes, access boundaries, canonical hashing, stable IDs, mutation authorization, and machine-checkable errors.
- **Stores** (`internal/store`): in-memory and SQLite implementations with 17 migrations, both held to one shared conformance suite (`storetest`). It covers transactions, immutability, graph integrity, restart, and bounded reads.
- **Call ledger** (`internal/invocation`): the durable PREPARED/SENT/COMPLETED/FAILED/UNKNOWN state machine with recovery and per-conversation reservations.
- **Directive parser** (`internal/directive`): a pure, source-gated, fuzzed parser. It keeps parse state from carrying across spans, so a fence or keyword split between spans has no effect.
- **Ingestion** (`internal/ingest`, `internal/graph`, `internal/policy`): an atomic pipeline covering idempotency receipts, snapshots, classification, duplicates, supersession, Working snapshots, and obligations.
- **Canonical examples** (`testdata/directives`): 82 golden cases, including 15 injection cases, checked byte for byte on both stores.
- **Live descriptor probes** (`probes/descriptor`): recorded evidence of Claude and OpenAI behavior for reasoning binding, cache reads, and compaction. This is the ground truth for capability descriptors.

## Beyond V1

V1 ships as an embedded Go library with a single-process SQLite store ([ADR 13](docs/adr/0013-deployment-model.md)). The goal is to prove the runtime's semantics first and scale out second. The intended direction after that:

- **`contextd` and a Context Protocol.** A context service behind a language-neutral protocol, so agent frameworks in Go, Python, and TypeScript can share one runtime. It needs its own ADR for authenticating principals across a process boundary.
- **Shared persistence.** A Postgres store that implements the same `Store` interface, once SQLite passes acceptance.
- **Redaction and retention** policy for stores that hold full tool output ([ADR 14](docs/adr/README.md)).

Explicitly deferred, though the interfaces leave room for them: a distributed context service, Temporal and Kubernetes integration, memory across sessions, vector search, and model-based classification or obligation matching. V1 search is deterministic and lexical.

## Repository layout

    .                          package contextruntime: public domain types and errors
    SDD.md                     V1 specification: requirements, invariants, phases, tests
    docs/sdd-event-traces.md   18 normative acceptance traces (T01–T18)
    docs/adr/                  architecture decision records
    docs/probes/               live provider probe reports and descriptor vocabulary
    internal/domain            pure types, enums, transitions, validation
    internal/directive         source-gated Markdown directive parser
    internal/ingest            ingestion pipeline
    internal/graph             provenance and supersession
    internal/policy            classification defaults and attribute rules
    internal/store             store contract; memory/, sqlite/, storetest/
    internal/invocation        durable provider-call ledger
    probes/descriptor          nested module: live Anthropic and OpenAI probes
    testdata/directives        canonical directive examples (part of the test contract)

## Development

This needs Go 1.26 or later. The module pins toolchain `go1.27.1`.

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...

# The probe module is outside ./...; its tests run offline.
cd probes/descriptor && go vet ./... && go test -race -count=1 ./...
```

CI runs on amd64 and arm64 Linux and on macOS, with Go 1.26 and 1.27. The architecture matrix matters because scores and cost estimates are fixed-point integers. The Go compiler fuses floating-point multiply-add on arm64 but not on amd64, and replay has to be identical on every machine.

Live descriptor probes call real provider APIs, need API keys, and cost money. The probe reports include the commands.

## How the project is built

Development is driven by the spec:

- **[SDD.md](SDD.md) is the contract.** Requirement IDs (`FR-*`, `NFR-*`, `INV-*`) are stable and are cited by the tests that enforce them.
- **[Normative event traces](docs/sdd-event-traces.md)** are acceptance cases that specify the semantic state and the next provider-visible request.
- **[ADRs](docs/adr/README.md) gate phases.** A phase cannot exit until its decisions are recorded and reviewed. If a decision conflicts with the spec, the ADR records the exact amendment.
- **Claims about provider behavior rest on recorded probes.** Each claim is labeled OBSERVED, DOCUMENTED, or ASSUMED. Unknown or stale profiles fail closed.
- **Canonical examples are part of the contract.** Any change that alters an `expected.json` counts as a parser version change and is reviewed as one.
