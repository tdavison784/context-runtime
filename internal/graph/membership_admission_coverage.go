package graph

import (
	"errors"
	"slices"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// RecordAdmissionCoverage stores the exact sources a trusted harness admitted
// to one recipient conversation as GENERATION_INPUT coverage at that
// conversation's boundary. Readability is checked for the recipient, not the
// harness. The ID derives from the request, so an identical retry returns the
// stored set and a changed set conflicts (P3-2/6/7).
func (s *MembershipService) RecordAdmissionCoverage(tx store.Tx, actor, recipient domain.Principal, requestID string, sources []domain.ItemContentRef) (result domain.CoverageRecord, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	if err = checkMembershipControl(tx.SessionID(), actor, recipient); err != nil {
		return result, err
	}
	if _, err = domain.MutationReceiptID(tx, actor, domain.MutationMembership, requestID); err != nil || len(sources) == 0 {
		return result, domain.ErrInvalidRecord
	}
	if len(sources) > s.policy.MaxCoverageMembers || len(sources) > s.policy.MaxTransactionWork-2 {
		return result, domain.ErrResourceLimit
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return result, err
	}
	conversation := domain.ConversationIDFor(recipient.TaskID, recipient.AgentID)
	id := membershipID("admission-coverage", tx.SessionID(), conversation+"\x00"+requestID)
	stored, err := sem.Coverage(id)
	replay := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return result, err
	}
	seq := stored.Seq
	if !replay {
		seq = tx.NextSeq()
	}
	c, members, err := s.planAdmissionCoverage(tx, recipient, id, seq, sources)
	if err != nil {
		return result, err
	}
	if replay {
		// A retry cannot change the admitted set; only the stored record answers.
		if c != stored {
			return result, domain.ErrEventIDConflict
		}
		return stored.Clone(), nil
	}
	if err = sem.InsertCoverage(c, members); err != nil {
		return result, err
	}
	return c.Clone(), nil
}

func (s *MembershipService) planAdmissionCoverage(tx store.ReadTx, recipient domain.Principal, id string, seq uint64, sources []domain.ItemContentRef) (domain.CoverageRecord, []domain.CoverageMember, error) {
	boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: recipient.SessionID, WorkflowID: recipient.WorkflowID, TaskID: recipient.TaskID, AgentID: recipient.AgentID}
	c := domain.CoverageRecord{SemanticMeta: membershipMeta(tx.SessionID(), id, seq), Purpose: domain.CoverageGenerationInput, Access: boundary}
	var members []domain.CoverageMember
	for _, ref := range sources {
		if err := ref.Validate(); err != nil {
			return c, nil, err
		}
		source, err := tx.Item(ref.ItemID)
		if errors.Is(err, domain.ErrNotFound) {
			return c, nil, domain.ErrNotFound
		}
		if err != nil {
			return c, nil, err
		}
		if source.SessionID != tx.SessionID() || !source.Access.Permits(recipient) {
			return c, nil, domain.ErrNotFound
		}
		if source.ContentHash != ref.ContentHash || domain.ContentHash(source.Parts) != source.ContentHash {
			return c, nil, domain.ErrIntegrity
		}
		add := []domain.CoverageMember{{Source: &ref}}
		if source.Role == domain.RoleProjection {
			deps, err := projectionDependencies(tx, recipient, source, boundary)
			if err != nil {
				return c, nil, err
			}
			add = append(add, deps...)
		}
		for _, m := range add {
			m.SemanticMeta = membershipMeta(tx.SessionID(), "", seq)
			m.CoverageID = id
			if m.ID, err = m.Key(); err != nil {
				return c, nil, err
			}
			members = append(members, m)
		}
	}
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { return strings.Compare(a.ID, b.ID) })
	members = slices.CompactFunc(members, func(a, b domain.CoverageMember) bool { return a.ID == b.ID })
	if len(members) > s.policy.MaxCoverageMembers {
		return c, nil, domain.ErrResourceLimit
	}
	c.MemberCount = uint64(len(members))
	var err error
	if c.Signature, err = domain.CoverageSignature(c, members); err != nil {
		return c, nil, err
	}
	return c, members, c.Validate()
}
