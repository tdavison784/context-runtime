package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type lifecycleCall struct {
	action domain.LifecycleAction
	actor  domain.Principal
	intent domain.ItemMutationIntent
	seq    uint64
}

// fakeLifecycle stands in for W3's lifecycle service to test ingest's
// command routing only: it authorizes at the actual sequence the way the
// Phase 2 preview did, applies the FR-DIR-005 effect with CAS and returns a
// frozen result. deny forces a refusal. It is never gate evidence.
type fakeLifecycle struct {
	calls *[]lifecycleCall
	deny  error
}

func (l fakeLifecycle) Resolve(tx store.Tx, actor domain.Principal, in domain.ResolveIntent, seq uint64) (LifecycleOutcome, error) {
	resolved, high := domain.GoalResolved, domain.RetentionHigh
	return l.apply(tx, domain.LifecycleResolve, actor, in, seq, domain.ItemChange{GoalStatus: &resolved, Retention: &high})
}

func (l fakeLifecycle) Unpin(tx store.Tx, actor domain.Principal, in domain.UnpinIntent, seq uint64) (LifecycleOutcome, error) {
	durable, high := domain.GenerationDurable, domain.RetentionHigh
	return l.apply(tx, domain.LifecycleUnpin, actor, in, seq, domain.ItemChange{Generation: &durable, Retention: &high})
}

func (l fakeLifecycle) apply(tx store.Tx, a domain.LifecycleAction, actor domain.Principal, in domain.ItemMutationIntent, seq uint64, ch domain.ItemChange) (LifecycleOutcome, error) {
	*l.calls = append(*l.calls, lifecycleCall{a, actor, in, seq})
	if l.deny != nil {
		return LifecycleOutcome{}, l.deny
	}
	before, err := tx.Item(in.ItemID)
	if err != nil {
		return LifecycleOutcome{}, err
	}
	// Authorize like the Phase 2 preview, but at the actual sequence, so a
	// routed command never gains authority the real service would deny.
	grants, err := tx.Grants()
	if err != nil {
		return LifecycleOutcome{}, err
	}
	action := domain.ActionResolve
	if a == domain.LifecycleUnpin {
		action = domain.ActionUnpin
	}
	auth, err := domain.AuthorizeMutation(domain.MutationRequest{Actor: actor, Action: action, Grants: grants, Seq: seq,
		Targets: []domain.MutationTarget{{ID: before.ID, Authority: before.Authority, Access: before.Access}}})
	if err != nil {
		return LifecycleOutcome{}, err
	}
	audit := "life_" + in.RequestID
	after, err := tx.UpdateItem(in.ItemID, in.ExpectedVersion, ch, domain.LifecycleEvent{
		ID: audit, SessionID: actor.SessionID, Seq: seq, TargetKind: domain.TargetItem, TargetID: in.ItemID, Action: string(a), Actor: actor})
	if err != nil {
		return LifecycleOutcome{}, err
	}
	observe := func(it domain.ContextItem) domain.ObservedItemState {
		return domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, Version: it.Version,
			Currentness: domain.ItemCurrent, GoalStatus: it.GoalStatus, Generation: it.Generation, Residency: it.Residency, Authority: it.Authority, Expiry: domain.ExpiryLive}
	}
	return LifecycleOutcome{MutationReceiptID: "mut_" + in.RequestID, GrantID: auth.GrantIDs[before.ID], Result: domain.ItemMutationResult{
		ItemID: in.ItemID, BeforeVersion: before.Version, AfterVersion: after.Version, Before: observe(before), After: observe(after), AuditID: audit}}, nil
}

// TestCommandsV2_ExecuteInSourceOrder (P3-35, FR-DIR-005): a Resolve parsed
// from a new Phase 3 event executes at its source position, as the span's
// source actor, at its own allocated sequence with CAS on the resolved
// version; a later command in the same event sees its effect. Nothing
// executes again on retry.
func TestCommandsV2_ExecuteInSourceOrder(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		var calls []lifecycleCall
		f.in.Lifecycle = fakeLifecycle{calls: &calls}
		sys := principal(domain.AuthoritySystem)
		e := sysEvent("cmd-order", "## Goal [g]\nShip.\n## Resolve [g]\n## Resolve [g]\n")
		r := f.mustIngest(sys, e)
		goal := mustDirective(t, r, "g")
		if len(calls) != 1 || calls[0].intent.ItemID != goal.ID || calls[0].intent.ExpectedVersion != 1 || calls[0].actor != sys || calls[0].seq <= goal.Seq {
			t.Fatalf("executor calls = %+v", calls)
		}
		// The receipt records the goal as the event left it: created, then
		// resolved in the same event.
		if goal.Version != 2 || *goal.GoalStatus != domain.GoalResolved {
			t.Errorf("receipt goal snapshot version %d status %s", goal.Version, *goal.GoalStatus)
		}
		if want, _ := domain.OperationRequestID(sys, calls[0].actor, r.OccurrenceID, r.Seq, 0, 1); calls[0].intent.RequestID != want {
			t.Errorf("request ID %q, want %q", calls[0].intent.RequestID, want)
		}
		if len(r.Lifecycle) != 2 {
			t.Fatalf("commands = %+v", r.Lifecycle)
		}
		first, second := r.Lifecycle[0], r.Lifecycle[1]
		if first.SchemaVersion != domain.LifecycleCommandSchemaV2 || first.Status != domain.CommandExecuted || first.Execution == nil ||
			first.Execution.Outcome != domain.CommandOutcomeExecuted || first.Execution.Result.After.GoalStatus == nil || *first.Execution.Result.After.GoalStatus != domain.GoalResolved {
			t.Errorf("first command = %+v", first)
		}
		if second.Status != domain.CommandNotExecuted || second.Resolution != domain.TargetMismatch || second.Execution.Outcome != domain.CommandOutcomeMismatch {
			t.Errorf("second command = %+v", second)
		}
		if len(r.MutationReceiptIDs) != 1 || r.MutationReceiptIDs[0] != first.Execution.MutationReceiptID {
			t.Errorf("receipt mutation IDs = %v", r.MutationReceiptIDs)
		}
		f.view(func(tx store.ReadTx) error {
			it, err := tx.Item(goal.ID)
			if err != nil || *it.GoalStatus != domain.GoalResolved || it.Residency != goal.Residency {
				t.Errorf("stored goal %+v (%v)", it, err)
			}
			return nil
		})
		f.mustIngest(sys, e)
		if len(calls) != 1 {
			t.Fatalf("retry executed the command again")
		}
	})
}

// TestCommandsV2_DetailRedaction (C-2, SEC-3.2): a viewer outside a
// command's detail boundary reads one uniform WITHHELD record, so a hidden
// successful Resolve is indistinguishable from a command naming nothing.
func TestCommandsV2_DetailRedaction(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.in.Lifecycle = fakeLifecycle{calls: new([]lifecycleCall)}
		f.gcTriggersOff() // commands under test, not the GC producer
		a := principal(domain.AuthorityUser)
		f.mustIngest(a, userEvent("priv", "## Pinned\n- [p] {scope=AGENT} private rule\n", true))
		hidden := f.mustIngest(a, userEvent("res-hidden", "## Unpin [p]\n", true))
		missing := f.mustIngest(a, userEvent("res-missing", "## Unpin [nosuch]\n", true))
		if hidden.Lifecycle[0].Status != domain.CommandExecuted || missing.Lifecycle[0].Resolution != domain.TargetNotFound {
			t.Fatalf("setup: %+v / %+v", hidden.Lifecycle[0], missing.Lifecycle[0])
		}
		b := a
		b.AgentID = "B"
		read := func(occurrence string) domain.LifecycleCommandRecord {
			var out []domain.LifecycleCommandRecord
			f.view(func(tx store.ReadTx) error {
				var err error
				out, err = tx.LifecycleCommands(store.CommandFilter{Viewer: b, OccurrenceID: occurrence})
				return err
			})
			if len(out) != 1 {
				t.Fatalf("B reads %d records for %s", len(out), occurrence)
			}
			return out[0]
		}
		h, m := read(hidden.OccurrenceID), read(missing.OccurrenceID)
		for _, rec := range []domain.LifecycleCommandRecord{h, m} {
			if rec.Status != domain.CommandWithheld || rec.Resolution != domain.TargetWithheld || rec.ResolvedItemID != "" ||
				rec.Execution == nil || !reflect.DeepEqual(*rec.Execution, domain.CommandExecutionDetail{Outcome: domain.CommandOutcomeWithheld}) {
				t.Errorf("B reads %+v", rec)
			}
		}
		if h.DetailAccess != m.DetailAccess || h.Access != m.Access {
			t.Errorf("boundaries distinguish the hidden success: %+v vs %+v", h, m)
		}
	})
}

// TestCommandsV2_AbortsAtomically (P3-35, R7): an unauthorized command,
// which the lifecycle executor refuses at its actual sequence, aborts the
// whole event with no command record; a resolvable command with
// no lifecycle executor fails closed. Unresolvable commands need no
// executor and are recorded as not executed.
func TestCommandsV2_AbortsAtomically(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys, user := principal(domain.AuthoritySystem), principal(domain.AuthorityUser)
		f.mustIngest(sys, sysEvent("g", "## Goal [G]\nShip.\n"))

		f.in.Lifecycle = nil
		f.gcTriggersOff() // the missing command executor must fail, not the GC producer
		f.requireAtomic(domain.ErrUnsupportedSchema, func() error {
			_, err := f.ingest(sys, sysEvent("no-exec", "## Remember\n- n\n## Resolve [G]\n"))
			return err
		})
		r := f.mustIngest(user, userEvent("nf", "## Resolve [nosuch]\n", true))
		if c := r.Lifecycle[0]; c.Status != domain.CommandNotExecuted || c.Execution.Outcome != domain.CommandOutcomeNotFound || len(c.Execution.Diagnostics) != 1 {
			t.Fatalf("NOT_FOUND record = %+v", c)
		}

		var calls []lifecycleCall
		f.in.Lifecycle = fakeLifecycle{calls: &calls}
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
			_, err := f.ingest(user, userEvent("deny", "## Remember\n- n\n## Resolve [G]\n", true))
			return err
		})
		// Resolution reads no grants; the executor denies at the actual
		// sequence and the whole event rolls back (P3-1).
		if len(calls) != 1 {
			t.Fatalf("executor calls = %d", len(calls))
		}
		f.in.Lifecycle = fakeLifecycle{calls: &calls, deny: domain.ErrInvalidAuthorityPromotion}
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
			_, err := f.ingest(sys, sysEvent("late-deny", "## Remember\n- n\n## Resolve [G]\n"))
			return err
		})
		if len(calls) != 2 || !errors.Is(f.in.Lifecycle.(fakeLifecycle).deny, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("executor calls = %d", len(calls))
		}
	})
}
