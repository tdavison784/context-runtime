package store

import "github.com/tdavison784/context-runtime/internal/domain"

type MembershipReader interface {
	LogicalExchange(id string) (domain.LogicalExchange, error)
	ExchangeAcknowledgment(id string) (domain.ExchangeAcknowledgment, error)
	ConversationMembership(conversationID string) (domain.ConversationMembershipState, error)
	ExchangesByConversation(conversationID string, page Page) (ResultPage[domain.LogicalExchange], error)
	ExchangeMembers(exchangeID string, page Page) (ResultPage[domain.ExchangeMember], error)
	MembershipsByItem(itemID string, page Page) (ResultPage[domain.ExchangeMember], error)
	// EarliestExchangeWithItem is the lowest-ordinal exchange of
	// conversationID with itemID as a member, ErrNotFound when none: one
	// keyed lookup through a write-time index, independent of conversation
	// length (H2, SPEC-2.7). Internal and unfiltered, like
	// MembershipsByItem.
	EarliestExchangeWithItem(conversationID, itemID string) (domain.LogicalExchange, error)
	AdmissionManifest(id string) (domain.AdmissionManifest, error)
	AdmissionsByExchange(exchangeID string, page Page) (ResultPage[domain.AdmissionManifest], error)
	Checkpoint(id string) (domain.Checkpoint, error)
	// Descending (Seq, ID), access filtered before limit; eligibility remains
	// W3/W5's job. Next is the last returned record, not an offset.
	CheckpointsByConversation(viewer domain.Principal, conversationID string, page Page) (ResultPage[domain.Checkpoint], error)
	OwnerRegistration(kind domain.OwnerKind, ownerID string) (domain.OwnerRegistration, error)
	// All-owner reads used only for completion, with fixed outward errors.
	OpenExchangesByTask(taskID string, page Page) (ResultPage[domain.LogicalExchange], error)
	ReservingCallsByTask(taskID string, page Page) (ResultPage[domain.CallRecord], error)
}
type MembershipWriter interface {
	InsertLogicalExchange(domain.LogicalExchange) error
	PutLogicalExchange(domain.LogicalExchange, uint64) (domain.LogicalExchange, error)
	InsertExchangeMember(domain.ExchangeMember) error
	InsertExchangeAcknowledgment(domain.ExchangeAcknowledgment) error
	InsertAdmissionManifest(domain.AdmissionManifest) error
	// CAS frontier requires every intervening ordinal to be closed with a
	// recorded consuming-call acknowledgment; cancellations are not coverage.
	PutConversationMembership(domain.ConversationMembershipState, uint64) (domain.ConversationMembershipState, error)
	InsertCheckpoint(domain.Checkpoint) error
	InsertOwnerRegistration(domain.OwnerRegistration) error
}
