package obligation

import (
	"errors"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReplacementHandoff names the exact occurrences of a replacement that graph
// already authorized and applied in the caller's transaction.
type ReplacementHandoff struct{ PriorID, ReplacementID string }

// DeclareForAuthorizedReplacementTx declares a Pinned replacement's slot-0
// obligation for an actor whose authority came from the supersession itself
// (XREV-1.2): an exact ActionReplaceDirective grant lets HARNESS replace a
// SYSTEM pin. Nothing in the handoff is trusted. It requires this
// transaction's SUPERSEDES edge ReplacementID→PriorID created at the actor's
// authority, and re-authorizes the actor for ActionReplaceDirective at the
// edge's own sequence: direct authority, or a live exact grant whose issuer
// has authority over the source. The replacement must preserve the prior's
// authority, key and boundary.
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
	if err := authorizedSupersession(tx, actor, prior, src, s.policy.MaxTargets); err != nil {
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

// authorizedSupersession proves prior→src was superseded in this
// transaction by an actor authorized for it. It reads only exact keys: the
// SUPERSEDES edge, then graph.AuthorizeAtSequence at the edge's own seq
// (direct authority, or a grant live at that seq whose issuer has authority
// over the source), so history length never matters (SPEC-2.6, DUR-2.11).
func authorizedSupersession(tx store.Tx, actor domain.Principal, prior, src domain.ContextItem, maxGrants int) error {
	rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: src.ID, ToID: prior.ID})
	if err != nil {
		return err
	}
	if len(rels) != 1 || !tx.Allocated(rels[0].Seq) || rels[0].Authority != actor.Authority {
		return domain.ErrInvalidRecord
	}
	target := domain.ItemGrantTarget(prior.SessionID, prior.ID)
	if _, err := graph.AuthorizeAtSequence(tx, actor, domain.ActionReplaceDirective, []domain.GrantTarget{target}, nil, rels[0].Seq, maxGrants); err != nil {
		return domain.ErrInvalidRecord
	}
	return nil
}
