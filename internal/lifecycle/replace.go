package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// RecordResult family for an explicit replacement. IDs are, in order, the new
// occurrence, its creation declaration, the superseded occurrence and, when
// a grant authorized the supersession, that grant.
const recordReplacement = "REPLACEMENT"

// ReplacementObligations is W4's in-transaction declaration of a replacement
// occurrence's claim (obligation.Service.DeclareForAuthorizedReplacementTx).
// It verifies the handoff against this transaction's supersession, so an
// exact ReplaceDirective grant carries over to the declaration (XREV-1.2),
// and reads the claim from the occurrence's creation declaration after the
// prior's obligations were retired; nil means no obligation was declared.
type ReplacementObligations interface {
	DeclareForAuthorizedReplacementTx(tx store.Tx, actor domain.Principal, h obligation.ReplacementHandoff, seq uint64) (*domain.ObligationRef, error)
}

var _ ReplacementObligations = (*obligation.Service)(nil)

// WithReplacementObligations returns a copy of s that declares Pinned and
// claim-bearing replacements through o, e.g. a W4 service with an embedder's
// matcher registry. The receiver is unchanged.
func (s *Service) WithReplacementObligations(o ReplacementObligations) *Service {
	c := *s
	c.obligations = o
	return &c
}

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
// the owning task's current turn. Identity-changing attributes and legacy
// sources without a creation declaration fail closed. A Pinned or
// claim-bearing source needs W4's declaration hook, which declares the new
// UNRESOLVED version (explicit or text-matched claim) after the prior's
// retirement; without the hook, or if an explicit claim declares nothing,
// the whole replacement fails.
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
		return s.replayReplacement(p, *prior)
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
	// W4 declares every Pinned occurrence's claim, explicit or matched from
	// its text, so no Pinned replacement proceeds without the hook.
	claim := was.ObligationDeclarationHash != "" || slices.ContainsFunc(was.AcceptedAttributes, func(a string) bool { return strings.HasPrefix(a, "obligation=") })
	pinned := old.Section == domain.SectionPinned && was.Generation == domain.GenerationPinned
	if (claim || pinned) && s.obligations == nil {
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
	if claim || pinned {
		ref, err := s.obligations.DeclareForAuthorizedReplacementTx(tx, p, obligation.ReplacementHandoff{PriorID: old.ID, ReplacementID: fresh.ID}, tx.NextSeq())
		if err != nil {
			return out, err
		}
		// A plain pin may declare nothing; an explicit claim must bind.
		if claim && ref == nil {
			return out, domain.ErrIntegrity
		}
	}
	// The supersession is a durable GC trigger (P3-39, SPEC-1.6); its
	// identity is the new occurrence, so the trigger fires exactly once.
	// A task-less directive's supersession produces no request (H4).
	if _, err = gcqueue.Enqueue(tx, s.policy, p, domain.GCSupersession, old.TaskID, fresh.ID); err != nil {
		return out, err
	}
	if out.GrantID, err = s.replacementGrant(tx, p, old, fresh); err != nil {
		return out, err
	}
	ids := []string{fresh.ID, created.ID, old.ID}
	if out.GrantID != "" {
		ids = append(ids, out.GrantID) // frozen for replay; no history read
	}
	out.Result.Records = &domain.RecordResult{Kind: recordReplacement, IDs: ids}
	if err = s.finish(tx, sem, p, domain.MutationLifecycle, method, i.RequestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(tx, p, domain.MutationLifecycle, i.RequestID)
	return out, err
}

func (s *Service) ReplaceDirectiveStandalone(ctx context.Context, p domain.Principal, i domain.ReplaceDirectiveIntent) (MutationOutcome, error) {
	var out MutationOutcome
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		if err := domain.ValidateCallerReplayID(i.RequestID); err != nil {
			return err
		}
		_, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(domain.ActionReplaceDirective), i.RequestID, i)
		if err != nil {
			return err
		}
		var seq uint64
		if prior == nil {
			// Callers never name runtime namespaces (H5, SEC-2.2); an
			// owner's committed receipt replays first (DUR-2.8).
			if err := domain.ValidateCallerRequestID(i.RequestID); err != nil {
				return err
			}
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

// replacementGrant returns the grant that authorized old's supersession in
// this transaction, re-authorizing at the SUPERSEDES edge's own sequence:
// exact keys only, so audit history length never matters (SPEC-2.6).
func (s *Service) replacementGrant(tx store.Tx, p domain.Principal, old, fresh domain.ContextItem) (string, error) {
	rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: fresh.ID, ToID: old.ID})
	if err != nil {
		return "", err
	}
	if len(rels) != 1 || !tx.Allocated(rels[0].Seq) {
		return "", domain.ErrIntegrity
	}
	target := domain.ItemGrantTarget(p.SessionID, old.ID)
	auth, err := graph.AuthorizeAtSequence(tx, p, domain.ActionReplaceDirective, []domain.GrantTarget{target}, nil, rels[0].Seq, s.policy.MaxTargets)
	if err != nil {
		return "", err
	}
	return auth.GrantIDs[target.AuthorizationKey], nil
}

// replayReplacement returns the frozen outcome; the authorizing grant, if
// any, is the receipt's fourth record ID.
func (s *Service) replayReplacement(p domain.Principal, receipt domain.MutationReceipt) (MutationOutcome, error) {
	rec := receipt.Result.Records
	if rec == nil || rec.Kind != recordReplacement || len(rec.IDs) != 3 && len(rec.IDs) != 4 || receipt.Principal != p {
		return MutationOutcome{}, domain.ErrIntegrity
	}
	grant := ""
	if len(rec.IDs) == 4 {
		grant = rec.IDs[3]
	}
	return MutationOutcome{MutationReceiptID: receipt.ID, GrantID: grant, Result: receipt.Result.Clone()}, nil
}
