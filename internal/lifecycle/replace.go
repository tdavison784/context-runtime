package lifecycle

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// RecordResult family for an explicit replacement. IDs are, in order, the new
// occurrence, its creation declaration and the superseded occurrence.
const recordReplacement = "REPLACEMENT"

// ReplaceDirective executes the explicit typed replacement (C-1): CAS on the
// expected current DIRECTIVE occurrence and version, an idempotent request
// identity, and a new occurrence that may reuse identical content. W1's graph
// supersedes the prior version, authorizes ActionReplaceDirective (direct or
// exact grant) at the edge's own seq and audits every indirect obligation
// retirement, each authorized separately; any failure aborts the event.
//
// The new occurrence keeps the prior's immutable identity (key, namespace,
// boundary, authority, owner, section, kind, scope, TTL) and restarts from the
// prior's declared creation defaults, so Unpin/Resolve/Archive state never
// carries over and a goal reopens as a new OPEN version. TURN/TTL origin is
// the owning task's current turn. Identity-changing attributes, legacy
// sources without a creation declaration, and obligation-declaring sources
// (whose new UNRESOLVED version is W4's declaration) fail closed.
func (s *Service) ReplaceDirective(tx store.Tx, p domain.Principal, i domain.ReplaceDirectiveIntent, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	method := string(domain.ActionReplaceDirective)
	sem, args, prior, err := s.begin(tx, p, domain.MutationLifecycle, method, i.RequestID, i)
	if err != nil {
		return out, err
	}
	if prior != nil {
		return s.replayReplacement(sem, p, *prior)
	}
	if err = i.Validate(); err != nil {
		return out, err
	}
	old, err := accessibleTarget(tx, p, i.ItemMutationIntent, seq)
	if err != nil {
		return out, err
	}
	if ns, keyed := old.DirectiveNamespace(); !keyed || ns != domain.NamespaceDirective {
		return out, domain.ErrNotFound
	}
	current, err := graph.IsCurrent(tx, old.ID)
	if err != nil {
		return out, err
	}
	if !current {
		return out, domain.ErrVersionConflict
	}
	decl, err := sem.CreationDeclaration(old.ID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && !decl.LegacyKnown {
		return out, domain.ErrUnsupportedSchema
	}
	if err != nil {
		return out, err
	}
	was := decl.AcceptedSemantics
	if was.ObligationDeclarationHash != "" {
		return out, domain.ErrUnsupportedSchema
	}
	attrs := slices.Clone(i.AcceptedAttributes)
	slices.Sort(attrs)
	if !slices.Equal(attrs, was.AcceptedAttributes) {
		return out, domain.ErrInvalidTransition
	}
	fresh, err := replacementItem(tx, i, old, was, seq)
	if err != nil {
		return out, err
	}
	if err = tx.InsertItem(fresh); err != nil {
		return out, err
	}
	created, err := graph.DeclareCreation(tx, fresh, graph.CreationAcceptance{PolicyVersion: decl.PolicyVersion, AcceptedAttributes: was.AcceptedAttributes})
	if err != nil {
		return out, err
	}
	previous, err := graph.ReplaceDirective(tx, p, fresh.TaskID, fresh.DirectiveID, fresh.ID, fresh.EventID)
	if err != nil {
		return out, err
	}
	if previous != old.ID {
		return out, domain.ErrVersionConflict
	}
	out.Result.Records = &domain.RecordResult{Kind: recordReplacement, IDs: []string{fresh.ID, created.ID, old.ID}}
	if out.GrantID, err = supersessionGrant(sem, p, old.ID, fresh.ID); err != nil {
		return out, err
	}
	if err = s.finish(tx, sem, p, domain.MutationLifecycle, method, i.RequestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(p.SessionID, domain.MutationLifecycle, i.RequestID)
	return out, err
}

func (s *Service) ReplaceDirectiveStandalone(ctx context.Context, p domain.Principal, i domain.ReplaceDirectiveIntent) (MutationOutcome, error) {
	var out MutationOutcome
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		_, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(domain.ActionReplaceDirective), i.RequestID, i)
		if err != nil {
			return err
		}
		var seq uint64
		if prior == nil {
			seq = tx.NextSeq()
		}
		out, err = s.ReplaceDirective(tx, p, i, seq)
		return err
	})
	if err != nil {
		return MutationOutcome{}, err
	}
	return out, nil
}

// replacementItem derives the new occurrence at seq. Its ID and EventID derive
// from the request identity; there is no transcript source and no wall clock.
func replacementItem(tx store.Tx, i domain.ReplaceDirectiveIntent, old domain.ContextItem, was domain.CreationSemantics, seq uint64) (domain.ContextItem, error) {
	n := old.Clone()
	enc := func(domainTag string) string {
		return domain.NewCanonicalEncoder(domainTag).String(old.SessionID).String(i.RequestID).String(old.ID).Hash()
	}
	n.ID, n.EventID = "item_"+enc("context-runtime/lifecycle-replacement-item/v1"), "replace_"+enc("context-runtime/lifecycle-replacement-event/v1")
	n.Seq, n.Version = seq, 1
	n.Parts = slices.Clone(i.Parts)
	n.ContentHash, n.SemanticBytes = domain.ContentHash(n.Parts), domain.SemanticBytes(n.Parts)
	n.Generation, n.Retention, n.Residency = was.Generation, was.Retention, was.Residency
	n.GoalStatus = nil
	if was.GoalStatus != nil {
		status := *was.GoalStatus
		n.GoalStatus = &status
	}
	n.Importance, n.LastUsedCall, n.AccessCount = 0, 0, 0
	// No transcript span, inherited classification metadata or wall clock:
	// the receipt's sequence is the audit time.
	n.CreatedAt, n.Tags, n.Source, n.SourceRanges = time.Time{}, nil, nil, nil
	if n.Scope == domain.ScopeTurn || n.TTLTurns != nil || n.CreatedTurn != 0 {
		task, err := tx.Task(n.TaskID)
		if err != nil {
			return domain.ContextItem{}, err
		}
		if task.Status != domain.TaskActive || task.Turn == 0 || task.TurnID == "" {
			return domain.ContextItem{}, domain.ErrInvalidTransition
		}
		n.CreatedTurn, n.TurnID = task.Turn, task.TurnID
	}
	return n, n.ValidateSemantic()
}

// supersessionGrant reads the immutable supersession audit graph wrote for
// old→fresh and returns the grant that authorized it, if any.
func supersessionGrant(r store.DeclarationReader, p domain.Principal, oldID, freshID string) (string, error) {
	var after store.Cursor
	for range 64 {
		page, err := r.LifecycleByTarget(domain.TargetItem, oldID, store.Page{After: after, Limit: 64})
		if err != nil {
			return "", err
		}
		for _, ev := range page.Records {
			if ev.Action == "superseded" && ev.From == oldID && ev.To == freshID {
				if ev.Actor != p {
					return "", domain.ErrIntegrity
				}
				return ev.GrantID, nil
			}
		}
		if !page.More {
			return "", domain.ErrIntegrity
		}
		after = page.Next
	}
	return "", domain.ErrResourceLimit
}

func (s *Service) replayReplacement(r store.DeclarationReader, p domain.Principal, receipt domain.MutationReceipt) (MutationOutcome, error) {
	rec := receipt.Result.Records
	if rec == nil || rec.Kind != recordReplacement || len(rec.IDs) != 3 {
		return MutationOutcome{}, domain.ErrIntegrity
	}
	grant, err := supersessionGrant(r, p, rec.IDs[2], rec.IDs[0])
	if err != nil {
		return MutationOutcome{}, err
	}
	return MutationOutcome{MutationReceiptID: receipt.ID, GrantID: grant, Result: receipt.Result.Clone()}, nil
}
