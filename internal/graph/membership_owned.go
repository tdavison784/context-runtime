package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Closure uses the recorded owner and originating turn. Advancing or finishing
// a task cannot turn a late acknowledgment into membership in a newer turn.
func readControlledExchange(sem store.MembershipReader, session string, actor domain.Principal, id string) (domain.LogicalExchange, error) {
	if actor.Validate() != nil || actor.SessionID != session || actor.Authority != domain.AuthoritySystem && actor.Authority != domain.AuthorityHarness {
		return domain.LogicalExchange{}, domain.ErrInvalidAuthorityPromotion
	}
	x, err := sem.LogicalExchange(id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.LogicalExchange{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.LogicalExchange{}, err
	}
	if checkMembershipControl(session, actor, x.Principal) != nil {
		return domain.LogicalExchange{}, domain.ErrNotFound
	}
	if x.Validate() != nil || x.ID != id || x.SessionID != session {
		return domain.LogicalExchange{}, domain.ErrIntegrity
	}
	return x, nil
}
