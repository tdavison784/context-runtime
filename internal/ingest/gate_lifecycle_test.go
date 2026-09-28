package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Gate steps that need W3's grant, completion and archive services. Grants
// are issued through ingest's GRANT operations; CompleteTask and Archive
// are standalone mutation entry points (not event operations), so the
// traces call W3's service directly against the state ingest produced.

// lifecycleService is W3's service over the fixture's store.
func (f *fixture) lifecycleService() *lifecycle.Service {
	f.t.Helper()
	svc, err := lifecycle.New(f.s, testPolicy())
	if err != nil {
		f.t.Fatal(err)
	}
	return svc
}

// grantEvent is a SYSTEM event issuing grant id for action on target to
// grantee, optionally expiring at expires.
func grantEvent(eventID, grantID string, action domain.Action, target domain.GrantTarget, grantee domain.Principal, expires uint64) domain.Event {
	return domain.Event{EventID: eventID, Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{Kind: domain.OperationGrant,
		Grant: &domain.GrantIntent{GrantID: grantID, Action: action, Targets: []domain.GrantTarget{target}, Grantee: &grantee, ExpiresAtSeq: expires}}}}
}

func obligationTarget(o domain.ObligationVersion) domain.GrantTarget {
	return domain.ObligationGrantTarget(o.SessionID, o.ObligationID, o.Version)
}

// TestGateT02_GrantBindsExactVersion (T02, P3-5/11): a HARNESS assertion
// grant issued through ingest for obligation v1 authorizes an attestation
// of v1 only; after the pin is replaced, the same grant cannot satisfy v2,
// and revoking it through ingest leaves the retired v1 untouched.
func TestGateT02_GrantBindsExactVersion(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		sys, harness := principal(domain.AuthoritySystem), principal(domain.AuthorityHarness)
		p1 := mustDirective(t, f.mustIngest(sys, sysEvent("p1", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v2.\n")), "dep")
		v1 := f.currentObligation(p1.ID)
		f.mustIngest(sys, grantEvent("g1", "grant-v1", domain.ActionAssertObligation, obligationTarget(v1), harness, 0))
		f.mustIngest(harness, transitionEvent("h-sat-v1", domain.EventHarness, v1, domain.ObligationSatisfied, domain.AssertionAttestation))
		if o := f.currentObligation(p1.ID); o.Status != domain.ObligationSatisfied {
			t.Fatalf("granted HARNESS attestation of v1 failed: %+v", o)
		}
		p2 := mustDirective(t, f.mustIngest(sys, sysEvent("p2", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v3.\n")), "dep")
		v2 := f.currentObligation(p2.ID)
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
			_, err := f.ingest(harness, transitionEvent("h-sat-v2", domain.EventHarness, v2, domain.ObligationSatisfied, domain.AssertionAttestation))
			return err
		})
		f.mustIngest(sys, domain.Event{EventID: "revoke", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{
			Kind: domain.OperationRevokeGrant, RevokeGrant: &domain.RevokeGrantIntent{GrantID: "grant-v1"}}}})
		f.view(func(tx store.ReadTx) error {
			versions, err := tx.ObligationVersions(v1.ObligationID)
			if err != nil || len(versions) != 2 || versions[0].Status != domain.ObligationSatisfied || versions[0].Current {
				t.Errorf("retired v1 after revocation: %+v (%v)", versions, err)
			}
			return nil
		})
	})
}

// TestGateT06_GrantExpiryAtActualSequence (T06, P3-1): a grant is
// evaluated at the operation's actual allocated sequence; once that passes
// its inclusive expiry, the assertion is denied atomically.
func TestGateT06_GrantExpiryAtActualSequence(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		sys, harness := principal(domain.AuthoritySystem), principal(domain.AuthorityHarness)
		o := f.currentObligation(mustDirective(t, f.mustIngest(sys, t06Setup()), "O").ID)
		// The grant expires two sequences after the one that issues it:
		// before any later event can act.
		expires := f.lastSeq() + 2
		f.mustIngest(sys, grantEvent("g-exp", "grant-exp", domain.ActionAssertObligation, obligationTarget(o), harness, expires))
		if f.lastSeq() < expires {
			f.mustIngest(sys, sysEvent("pad", "Advance the sequence."))
		}
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
			_, err := f.ingest(harness, transitionEvent("h-late", domain.EventHarness, o, domain.ObligationSatisfied, domain.AssertionAttestation))
			return err
		})
	})
}

// TestGateT06_Completion (T06, P3-9, C-3/X8): a USER cannot complete a task
// holding a SYSTEM goal; SYSTEM cannot complete while O is unresolved, not
// even after Unpin or disabling its materialization; a task-owned goal the
// completer cannot see blocks completion with the same fixed error; once
// O is satisfied SYSTEM completion resolves G, closes the task, and replays.
func TestGateT06_Completion(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		if !hasCompletion(f.s) {
			t.Skip("GATE-PENDING: needs W2 owner-goal (OpenGoalsByTaskOwner) and GC request facet records for W3 CompleteTask")
		}
		svc := f.lifecycleService()
		sys, user := principal(domain.AuthoritySystem), principal(domain.AuthorityUser)
		r := f.mustIngest(sys, t06Setup())
		g, pin := mustDirective(t, r, "G"), mustDirective(t, r, "O")
		complete := func(p domain.Principal, req string) error {
			_, err := svc.CompleteTaskStandalone(ctx, p, domain.CompleteTaskIntent{RequestID: req, TaskID: "T"})
			return err
		}
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error { return complete(user, "c-user") })
		f.requireAtomic(domain.ErrUnfinishedObligations, func() error { return complete(sys, "c-sys-1") })

		f.mustIngest(sys, sysEvent("unpin", "## Unpin [O]\n"))
		f.requireAtomic(domain.ErrUnfinishedObligations, func() error { return complete(sys, "c-sys-2") })
		o := f.currentObligation(pin.ID)
		f.mustIngest(sys, domain.Event{EventID: "mat", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{Kind: domain.OperationMaterialization,
			Materialization: &domain.SetObligationMaterializationIntent{Target: domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}, ExpectedRevision: o.Revision, Disabled: true}}}})
		f.requireAtomic(domain.ErrUnfinishedObligations, func() error { return complete(sys, "c-sys-3") })

		f.mustIngest(sys, transitionEvent("sat", domain.EventSystem, f.currentObligation(pin.ID), domain.ObligationSatisfied, domain.AssertionAttestation))
		// A task-owned goal only agent B's boundary can see blocks
		// completion by agent A with the fixed authority error.
		b := principal(domain.AuthorityUser)
		b.AgentID = "B"
		hidden := domain.Span{Authority: domain.AuthorityUser, DirectiveCapable: true,
			Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T", AgentID: "B"},
			Parts:  []domain.InputPart{{Type: domain.PartText, MediaType: "text/plain", Text: "## Goal [hidden]\nB's private goal.\n"}}}
		hr := f.mustIngest(b, domain.Event{EventID: "hidden", Kind: domain.EventUser, Spans: []domain.Span{hidden}})
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error { return complete(sys, "c-sys-hidden") })
		f.mustIngest(b, userEvent("hidden-res", "## Resolve [hidden]\n", true))
		if f.isCurrent(mustDirective(t, hr, "hidden").ID) {
			f.view(func(tx store.ReadTx) error {
				it, err := tx.Item(mustDirective(t, hr, "hidden").ID)
				if err != nil || *it.GoalStatus != domain.GoalResolved {
					t.Fatalf("B's goal not resolved: %+v (%v)", it.GoalStatus, err)
				}
				return nil
			})
		}

		if err := complete(sys, "c-sys-ok"); err != nil {
			t.Fatalf("SYSTEM completion: %v", err)
		}
		seq := f.lastSeq()
		if ts := f.task(); ts.Status != domain.TaskCompleted {
			t.Fatalf("task = %+v", ts)
		}
		f.view(func(tx store.ReadTx) error {
			it, err := tx.Item(g.ID)
			if err != nil || *it.GoalStatus != domain.GoalResolved {
				t.Errorf("G after completion: %+v (%v)", it.GoalStatus, err)
			}
			return nil
		})
		if err := complete(sys, "c-sys-ok"); err != nil || f.lastSeq() != seq {
			t.Fatalf("completion retry: %v, seq %d -> %d", err, seq, f.lastSeq())
		}
		if err := complete(sys, "c-sys-again"); err == nil || errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Fatalf("a new completion request on a completed task: %v", err)
		}
	})
}
