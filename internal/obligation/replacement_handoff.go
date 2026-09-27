package obligation

import (
	"errors"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReplacementHandoff names the exact occurrences of a replacement that graph
// already authorized and applied in the caller's transaction.
type ReplacementHandoff struct{ PriorID, ReplacementID string }

// DeclareForAuthorizedReplacementTx declares a Pinned replacement's slot-0
// obligation for an actor whose authority came from the supersession itself
// (XREV-1.2): an exact ActionReplaceDirective grant lets HARNESS replace a
// SYSTEM pin. Nothing in the handoff is trusted. It requires this
// transaction's SUPERSEDES edge ReplacementID→PriorID and its "superseded"
// audit by actor, and re-authorizes the recorded grant with
// domain.AuthorizeMutation at the edge's own sequence (live, exact target,
// matching grantee, issuer authority over the source). Without a grant the
// actor needs direct authority, exactly as DeclareForReplacementTx. The
// replacement must preserve the prior's authority, key and boundary.
func (s *Service) DeclareForAuthorizedReplacementTx(tx store.Tx, actor domain.Principal, h ReplacementHandoff, seq uint64) (*domain.ObligationRef, error) {
	sem, err := beginAt(tx, actor, seq)
	if err != nil {
		return nil, err
	}
	prior, err := tx.Item(h.PriorID)
	if err != nil || !prior.Access.Permits(actor) {
		return nil, notFound(err)
	}
	src, err := tx.Item(h.ReplacementID)
	if err != nil || !src.Access.Permits(actor) {
		return nil, notFound(err)
	}
	if src.Section != domain.SectionPinned || src.Generation != domain.GenerationPinned || !tx.Allocated(src.Seq) ||
		src.Authority != prior.Authority || src.DirectiveID != prior.DirectiveID || src.TaskID != prior.TaskID || src.Access != prior.Access || src.Namespace != prior.Namespace {
		return nil, domain.ErrInvalidRecord
	}
	if err := authorizedSupersession(tx, sem, actor, prior, src); err != nil {
		return nil, err
	}
	decl, err := sem.CreationDeclaration(src.ID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && !decl.LegacyKnown {
		return nil, domain.ErrUnsupportedSchema
	}
	if err != nil {
		return nil, err
	}
	claim := ""
	for _, a := range decl.AcceptedSemantics.AcceptedAttributes {
		v, ok := strings.CutPrefix(a, "obligation=")
		if !ok {
			continue
		}
		if claim != "" || !domain.ValidAttributeValue(v) {
			return nil, domain.ErrInvalidRecord
		}
		claim = v
	}
	return s.declarePinned(tx, sem, actor, src, claim, seq)
}

// authorizedSupersession proves prior→src was superseded in this transaction
// by actor, under direct authority or the exact grant graph recorded.
func authorizedSupersession(tx store.Tx, sem store.SemanticReader, actor domain.Principal, prior, src domain.ContextItem) error {
	rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: src.ID, ToID: prior.ID})
	if err != nil {
		return err
	}
	if len(rels) != 1 || !tx.Allocated(rels[0].Seq) {
		return domain.ErrInvalidRecord
	}
	edge := rels[0]
	audit, err := supersessionAudit(tx, sem, prior.ID, src.ID)
	if err != nil {
		return err
	}
	if audit.Actor != actor {
		return domain.ErrInvalidRecord
	}
	if audit.GrantID == "" {
		if !actor.Authority.CanHoldLifecycleAuthority() || !actor.Authority.AtLeast(src.Authority) {
			return domain.ErrInvalidRecord
		}
		return nil
	}
	g, err := tx.Grant(audit.GrantID)
	if err != nil {
		return notFound(err)
	}
	target := domain.MutationTarget{Ref: domain.ItemGrantTarget(prior.SessionID, prior.ID), ID: prior.ID, Authority: prior.Authority, Access: prior.Access}
	auth, err := domain.AuthorizeMutation(domain.MutationRequest{Actor: actor, Action: domain.ActionReplaceDirective, Targets: []domain.MutationTarget{target}, Grants: []domain.MutationGrant{g}, Seq: edge.Seq})
	if err != nil || auth.GrantIDs[target.AuthorizationID()] != g.ID {
		return domain.ErrInvalidRecord
	}
	return nil
}

// supersessionAudit finds the "superseded" audit graph wrote in this
// transaction for prior→src; the audit is immutable and bounded per item.
func supersessionAudit(tx store.Tx, sem store.SemanticReader, priorID, srcID string) (domain.LifecycleEvent, error) {
	var after store.Cursor
	for range 64 {
		page, err := sem.LifecycleByTarget(domain.TargetItem, priorID, store.Page{After: after, Limit: 64})
		if err != nil {
			return domain.LifecycleEvent{}, err
		}
		for _, ev := range page.Records {
			if ev.Action == "superseded" && ev.From == priorID && ev.To == srcID && tx.Allocated(ev.Seq) {
				return ev, nil
			}
		}
		if !page.More {
			return domain.LifecycleEvent{}, domain.ErrInvalidRecord
		}
		after = page.Next
	}
	return domain.LifecycleEvent{}, domain.ErrResourceLimit
}
