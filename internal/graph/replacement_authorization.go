package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The SUPERSEDES edge is the source-currentness mutation; its reserved sequence
// governs the grant. The later audit refers to that exact edge via From/To/EventID.
// Each indirect obligation retirement separately authorizes its own audit seq.
func authorizeReplacement(tx store.Tx, actor domain.Principal, fresh, prior domain.ContextItem, seq uint64) (string, error) {
	err := domain.AuthorizeSupersession(actor, fresh, prior)
	if err != nil {
		// A delegated replacement preserves the source authority and immutable
		// namespace/key/owner boundary; it cannot create a higher-authority key.
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) || !actor.Authority.CanHoldLifecycleAuthority() ||
			fresh.Namespace != domain.NamespaceDirective || prior.Namespace != domain.NamespaceDirective ||
			fresh.Authority != prior.Authority || fresh.DirectiveID != prior.DirectiveID ||
			fresh.SessionID != prior.SessionID || fresh.TaskID != prior.TaskID || fresh.WorkflowID != prior.WorkflowID || fresh.AgentID != prior.AgentID || fresh.Access != prior.Access {
			return "", err
		}
	}
	if actor.Authority == domain.AuthorityAgent {
		return "", nil
	} // narrow owner exception, checked above
	target := domain.ItemGrantTarget(tx.SessionID(), prior.ID)
	auth, err := AuthorizeAtSequence(tx, actor, domain.ActionReplaceDirective, []domain.GrantTarget{target}, nil, seq, maxBoundObligations)
	if err != nil {
		return "", err
	}
	return auth.GrantIDs[target.AuthorizationKey], nil
}
