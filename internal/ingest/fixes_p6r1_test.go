package ingest

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Regression tests for PR #6 review round 1 (ingest findings).

// TestEmptyOperationStreamRejected_G4 is SEC-1.11/SPEC-1.2 (G4, P3-34/40):
// an event with spans and an empty, non-nil Operations stream is malformed
// and aborts with nothing written, rather than committing an envelope whose
// spans were never ingested. The nil form still ingests every span, and it
// is not locked out by the rejected empty form (nil-vs-empty round trip).
func TestEmptyOperationStreamRejected_G4(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("g4-empty-ops", "hello there", false)
		e.Operations = []domain.SemanticOperation{}
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(user, e)
			return err
		})
		e.Operations = nil
		r := f.mustIngest(user, e)
		if len(r.ItemIDs()) != 1 {
			t.Fatalf("nil stream ingested %d items, want 1", len(r.ItemIDs()))
		}
		again, err := f.ingest(user, e)
		if err != nil || !reflect.DeepEqual(again, r) {
			t.Fatalf("nil-stream retry = %v", err)
		}
		e.Operations = []domain.SemanticOperation{}
		if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrInvalidRecord) && !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("empty-stream retry of the nil form = %v, want rejection", err)
		}
	})
}

// TestPlainIngestCannotClaimOutcomeEventIDs_SEC14 is SEC-1.4 (G3, R20.1):
// the outcome- EventID namespace belongs to the call ledger's outcome path.
// A plain Ingest or Apply naming a call's output EventID, one of its tool
// results, or any other outcome- ID aborts with nothing written, so it can
// neither squat the canonical ID (making the real outcome conflict) nor be
// replayed by the outcome path as if it had been registered.
func TestPlainIngestCannotClaimOutcomeEventIDs_SEC14(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		tool := toolResultEvent(b, "run-tests", "PASS 12/12")
		other := userEvent("outcome-chosen", "hello", false)
		for _, tc := range map[string]struct {
			p domain.Principal
			e domain.Event
		}{
			"output ID":      {agent, out},
			"tool result ID": {agent, tool},
			"harness tool":   {dispatcherFor(agent), tool},
			"any outcome-":   {principal(domain.AuthorityUser), other},
		} {
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				_, err := f.ingest(tc.p, tc.e)
				return err
			})
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				return f.s.Update(ctx, sess, func(tx store.Tx) error {
					_, err := f.in.Apply(tx, tc.p, tc.e, "")
					return err
				})
			})
		}
		// The real outcome and its tool result still register under their
		// bindings.
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"run-tests"}}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, out, m); err != nil {
			t.Fatal(err)
		}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, tool, &OutcomeMembership{Dispatcher: dispatcherFor(agent)}); err != nil {
			t.Fatal(err)
		}
		if _, ms := f.members(b.ExchangeID); len(ms) != 3 {
			t.Fatalf("members = %d, want output, tool call and tool result", len(ms))
		}
	})
}

// TestOutcomeMembershipIsRequestIdentity_DUR17 is DUR-1.7 (G3): an
// outcome's membership (dispatcher and ordered tool call IDs, or none) is
// part of its request identity. A retry with different membership
// conflicts with nothing written instead of silently succeeding; an
// untrusted dispatcher is refused even on the retry path; the exact retry
// replays its receipt.
func TestOutcomeMembershipIsRequestIdentity_DUR17(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"call-a"}}
		first, err := f.in.IngestOutcome(ctx, f.s, b, out, m)
		if err != nil {
			t.Fatal(err)
		}
		selfDispatch := &OutcomeMembership{Dispatcher: agent, ToolCallIDs: []string{"call-a"}}
		for _, tc := range map[string]struct {
			m    *OutcomeMembership
			want error
		}{
			"other tool calls":  {&OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"call-x", "call-y"}}, domain.ErrEventIDConflict},
			"reordered/dropped": {&OutcomeMembership{Dispatcher: dispatcherFor(agent)}, domain.ErrEventIDConflict},
			"no membership":     {nil, domain.ErrEventIDConflict},
			"agent dispatcher":  {selfDispatch, domain.ErrInvalidAuthorityPromotion},
		} {
			f.requireAtomic(tc.want, func() error {
				_, err := f.in.IngestOutcome(ctx, f.s, b, out, tc.m)
				return err
			})
		}
		seq := f.lastSeq()
		again, err := f.in.IngestOutcome(ctx, f.s, b, out, m)
		if err != nil || !reflect.DeepEqual(again, first) || f.lastSeq() != seq {
			t.Fatalf("exact retry = %v (equal %v)", err, reflect.DeepEqual(again, first))
		}
	})
	semanticStores(t, func(t *testing.T, f *fixture) {
		// Without membership first, a retry that adds one conflicts rather
		// than reporting success for a round that can never progress.
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		if _, err := f.in.IngestOutcome(ctx, f.s, b, out, nil); err != nil {
			t.Fatal(err)
		}
		f.requireAtomic(domain.ErrEventIDConflict, func() error {
			_, err := f.in.IngestOutcome(ctx, f.s, b, out, &OutcomeMembership{Dispatcher: dispatcherFor(agent)})
			return err
		})
	})
}

// ownerRegistration reads the (kind, id) owner registration, if any.
func (f *fixture) ownerRegistration(kind domain.OwnerKind, id string) (domain.OwnerRegistration, bool) {
	f.t.Helper()
	var o domain.OwnerRegistration
	var found bool
	f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		o, err = sem.OwnerRegistration(kind, id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		found = err == nil
		return err
	})
	return o, found
}

// TestOwnerRegistrationOnFirstTrustedAssociation_SPEC17 is SPEC-1.7
// (P3-32/C-15): the first item an authenticated principal creates at
// WORKFLOW or AGENT scope records that owner's immutable registration in
// the same transaction, before the item, as the trusted ingestion service
// (HARNESS), with the owner ID taken from the principal and the event's
// occurrence as its source. The item's scope lifetime is then LIVE rather
// than UNKNOWN, and stays LIVE after its originating task completes. A
// later association does not re-register (and does not abort), and a
// TOOL-authority item never registers an owner.
func TestOwnerRegistrationOnFirstTrustedAssociation_SPEC17(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		if _, ok := f.ownerRegistration(domain.OwnerWorkflow, "wf"); ok {
			t.Fatal("registration before any association")
		}
		// A TOOL-authority span at AGENT scope associates no owner. (A tool
		// result needs an opened turn; the plain question is TASK-scoped.)
		f.mustIngest(user, userEvent("q", "hi", false))
		if _, ok := f.ownerRegistration(domain.OwnerAgent, "A"); ok {
			t.Fatal("a TASK-scoped item registered its agent owner")
		}
		tool := agentPrincipal()
		te := domain.Event{EventID: "tool-1", Kind: domain.EventTool, Spans: []domain.Span{textSpan(domain.AuthorityTool, false, "tool output")}}
		te.Spans[0].Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, WorkflowID: "wf", TaskID: "T", AgentID: "A"}
		te.Spans[0].Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "tool:x", ToolCallID: "x"}
		f.mustIngest(tool, te)
		if _, ok := f.ownerRegistration(domain.OwnerAgent, "A"); ok {
			t.Fatal("a TOOL item registered its agent owner")
		}

		r := f.mustIngest(user, userEvent("own-1", "## Pinned\n- [w] {scope=WORKFLOW} Workflow rule.\n- [a] {scope=AGENT} Agent rule.\n", true))
		for _, tc := range []struct {
			kind domain.OwnerKind
			id   string
		}{{domain.OwnerWorkflow, "wf"}, {domain.OwnerAgent, "A"}} {
			o, ok := f.ownerRegistration(tc.kind, tc.id)
			if !ok {
				t.Fatalf("%s owner not registered", tc.kind)
			}
			if o.Actor.Authority != domain.AuthorityHarness || o.Actor.SessionID != sess || o.SourceID != r.OccurrenceID || o.WorkflowID != "wf" || o.Seq == 0 || o.Seq > r.Items[0].Seq {
				t.Fatalf("%s registration = %+v", tc.kind, o)
			}
		}
		var scoped []domain.ContextItem
		for _, it := range r.Items {
			if it.Scope == domain.ScopeWorkflow || it.Scope == domain.ScopeAgent {
				scoped = append(scoped, it)
			}
		}
		if len(scoped) != 2 {
			t.Fatalf("scoped items = %d", len(scoped))
		}
		lifetime := func(it domain.ContextItem) domain.ExpiryState {
			kind, id := domain.OwnerWorkflow, it.Access.WorkflowID
			if it.Scope == domain.ScopeAgent {
				kind, id = domain.OwnerAgent, it.Access.AgentID
			}
			o, _ := f.ownerRegistration(kind, id)
			task := f.task()
			return policy.ScopeLifetime(it, policy.OwnerSnapshot{Seq: f.lastSeq(), Task: &task, Owner: &o})
		}
		for _, it := range scoped {
			if got := lifetime(it); got != domain.ExpiryLive {
				t.Fatalf("%s item lifetime = %s, want LIVE", it.Scope, got)
			}
		}
		// A later association neither re-registers nor aborts.
		before, _ := f.ownerRegistration(domain.OwnerWorkflow, "wf")
		f.mustIngest(user, userEvent("own-2", "## Pinned\n- [w2] {scope=WORKFLOW} Another workflow rule.\n", true))
		if after, _ := f.ownerRegistration(domain.OwnerWorkflow, "wf"); !reflect.DeepEqual(after, before) {
			t.Fatalf("registration changed: %+v -> %+v", before, after)
		}
		// The owner outlives its originating task (P3-32).
		if hasCompletion(f.s) {
			if _, err := f.lifecycleService().CompleteTaskStandalone(ctx, principal(domain.AuthoritySystem), domain.CompleteTaskIntent{RequestID: "c-own", TaskID: "T"}); err != nil {
				t.Fatalf("complete: %v", err)
			}
			if f.task().Status != domain.TaskCompleted {
				t.Fatal("task not completed")
			}
			for _, it := range scoped {
				if got := lifetime(it); got != domain.ExpiryLive {
					t.Fatalf("after completion, %s item lifetime = %s, want LIVE", it.Scope, got)
				}
			}
		}
	})
}
