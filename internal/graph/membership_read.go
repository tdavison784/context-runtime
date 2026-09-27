package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReadClosedExchangePrefix reads one consistent snapshot for the authenticated
// conversation recipient. It requires all earlier groups to be acknowledged,
// excludes the issuing/open group, and returns no partial prefix on failure.
// workLimit counts record reads, including acknowledgment/manifest/call checks.
func ReadClosedExchangePrefix(tx store.ReadTx, p domain.Principal, issuingID string, pageSize, workLimit int) (ClosedPrefixSnapshot, error) {
	fail := func(err error) (ClosedPrefixSnapshot, error) { return ClosedPrefixSnapshot{}, err }
	if p.Validate() != nil || p.SessionID != tx.SessionID() || p.TaskID == "" || p.AgentID == "" {
		return fail(domain.ErrNotFound)
	}
	if pageSize <= 0 || workLimit <= 2 {
		return fail(domain.ErrResourceLimit)
	}
	sem, err := store.ReadSemantic(tx)
	if err != nil {
		return fail(err)
	}
	issuing, err := sem.LogicalExchange(issuingID)
	if errors.Is(err, domain.ErrNotFound) {
		return fail(domain.ErrNotFound)
	}
	if err != nil {
		return fail(err)
	}
	if issuing.Principal != p || issuing.SessionID != p.SessionID || issuing.ConversationID != domain.ConversationIDFor(p.TaskID, p.AgentID) {
		return fail(domain.ErrNotFound)
	}
	if issuing.ID != issuingID {
		return fail(domain.ErrIncompleteCoverage)
	}
	state, err := sem.ConversationMembership(issuing.ConversationID)
	if err != nil {
		return fail(incompleteMembership(err))
	}
	all, err := collectMembershipPages(pageSize, workLimit-2, func(page store.Page) (store.ResultPage[domain.LogicalExchange], error) {
		return sem.ExchangesByConversation(issuing.ConversationID, page)
	})
	if err != nil {
		return fail(err)
	}
	prefix, err := closedMembershipPrefix(state, issuing, all)
	if err != nil {
		return fail(err)
	}
	if len(prefix.Exchanges) > (workLimit-2-len(all))/3 {
		return fail(domain.ErrResourceLimit)
	}
	for _, x := range prefix.Exchanges {
		if err := readMembershipAcknowledgment(tx, sem, x, state.Revision); err != nil {
			return fail(err)
		}
	}
	return prefix, nil
}

func readMembershipAcknowledgment(tx store.ReadTx, sem store.MembershipReader, x domain.LogicalExchange, revision uint64) error {
	ack, err := sem.ExchangeAcknowledgment(x.AcknowledgmentID)
	if err != nil {
		return incompleteMembership(err)
	}
	manifest, err := sem.AdmissionManifest(ack.ManifestID)
	if err != nil {
		return incompleteMembership(err)
	}
	call, err := tx.Call(ack.ConsumingCallID)
	if err != nil {
		return incompleteMembership(err)
	}
	if manifest.MembershipRevision > revision {
		return domain.ErrIncompleteCoverage
	}
	return checkMembershipAcknowledgment(x, ack, manifest, call)
}

func incompleteMembership(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrIncompleteCoverage
	}
	return err
}
