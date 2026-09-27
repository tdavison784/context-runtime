package graph

import (
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// CoverClosedPrefix reads the recipient's authenticated closed prefix before
// issuingID and stores it as EXCHANGE_REPLACEMENT coverage naming every
// exchange explicitly; the frontier never substitutes for member IDs. An empty
// prefix covers nothing and is rejected (P3-6/7/27). key makes the coverage
// identity unique to its producing request.
func (s *MembershipService) CoverClosedPrefix(tx store.Tx, p domain.Principal, issuingID, key string) (result domain.CoverageRecord, prefix ClosedPrefixSnapshot, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	prefix, err = ReadClosedExchangePrefix(tx, p, issuingID, s.policy.MaxPageSize, s.policy.MaxTransactionWork)
	if err != nil {
		return result, prefix, err
	}
	if len(prefix.Exchanges) == 0 {
		return result, prefix, domain.ErrIncompleteCoverage
	}
	if len(prefix.Exchanges) > s.policy.MaxCoverageMembers {
		return result, prefix, domain.ErrResourceLimit
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return result, prefix, err
	}
	id := membershipID("closed-prefix", tx.SessionID(), prefix.ConversationID+"\x00"+key)
	seq := tx.NextSeq()
	result = domain.CoverageRecord{
		SemanticMeta: membershipMeta(tx.SessionID(), id, seq), Purpose: domain.CoverageExchangeReplacement,
		Access:         domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID},
		ConversationID: prefix.ConversationID, MembershipRevision: prefix.MembershipRevision, ClosedFrontier: prefix.ClosedFrontier,
		MemberCount: uint64(len(prefix.Exchanges)),
	}
	members := make([]domain.CoverageMember, 0, len(prefix.Exchanges))
	for _, x := range prefix.Exchanges {
		m := domain.CoverageMember{SemanticMeta: membershipMeta(tx.SessionID(), "", seq), CoverageID: id, ExchangeID: x.ID}
		if m.ID, err = m.Key(); err != nil {
			return domain.CoverageRecord{}, prefix, err
		}
		members = append(members, m)
	}
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { return strings.Compare(a.ID, b.ID) })
	if result.Signature, err = domain.CoverageSignature(result, members); err != nil {
		return domain.CoverageRecord{}, prefix, err
	}
	if err = sem.InsertCoverage(result, members); err != nil {
		return domain.CoverageRecord{}, prefix, err
	}
	return result.Clone(), prefix, nil
}
