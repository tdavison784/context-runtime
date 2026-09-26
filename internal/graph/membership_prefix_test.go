package graph

import (
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func membershipPrefixFixture() (domain.ConversationMembershipState, []domain.LogicalExchange) {
	p := domain.Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: domain.AuthorityAgent}
	meta := func(id string, seq uint64) domain.SemanticMeta {
		return domain.SemanticMeta{ID: id, SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: seq}
	}
	state := domain.ConversationMembershipState{SemanticMeta: meta("membership", 10), ConversationID: domain.ConversationIDFor("t", "a"), Revision: 6, LastOrdinal: 3, ClosedFrontier: 2}
	var xs []domain.LogicalExchange
	for n := uint64(1); n <= 3; n++ {
		xs = append(xs, domain.LogicalExchange{SemanticMeta: meta(fmt.Sprint("x", n), n), ConversationID: state.ConversationID, Ordinal: n, Principal: p, TurnID: "turn", Turn: 1, State: domain.ExchangeClosed, AcknowledgmentID: fmt.Sprint("ack", n), Revision: 2})
	}
	xs[2].State, xs[2].AcknowledgmentID = domain.ExchangeOpen, ""
	return state, xs
}

func TestMembershipPrefixExcludesIssuingRound(t *testing.T) {
	state, xs := membershipPrefixFixture()
	// Storage pages are in sequence order, which need not be ordinal order
	// after closure CAS updates. Neither input order nor caller data is mutated.
	reversed := []domain.LogicalExchange{xs[2], xs[0], xs[1]}
	got, err := closedMembershipPrefix(state, xs[2], reversed)
	if err != nil || !slices.Equal(got.Exchanges, xs[:2]) || got.MembershipRevision != state.Revision || got.ClosedFrontier != 2 || reversed[0] != xs[2] {
		t.Fatalf("prefix: %+v, %v", got, err)
	}
}

func TestMembershipPrefixRejectsGapsAndFalseClosure(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*domain.ConversationMembershipState, []domain.LogicalExchange) []domain.LogicalExchange
	}{
		{"missing middle", func(_ *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			return append(xs[:1], xs[2:]...)
		}},
		{"duplicate ordinal", func(_ *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			xs[1].Ordinal = 1
			return xs
		}},
		{"cancelled", func(_ *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			xs[1].State = domain.ExchangeCancelled
			return xs
		}},
		{"unacknowledged", func(_ *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			xs[1].AcknowledgmentID = ""
			return xs
		}},
		{"other agent", func(_ *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			xs[1].Principal.AgentID = "b"
			return xs
		}},
		{"open gap", func(s *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			s.ClosedFrontier = 1
			return xs
		}},
		{"covers issuing round", func(s *domain.ConversationMembershipState, xs []domain.LogicalExchange) []domain.LogicalExchange {
			s.ClosedFrontier = 3
			return xs
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, xs := membershipPrefixFixture()
			issuing := xs[2]
			xs = tc.edit(&state, xs)
			if got, err := closedMembershipPrefix(state, issuing, xs); err != domain.ErrIncompleteCoverage || got.Exchanges != nil {
				t.Fatalf("invalid prefix returned: %+v, %v", got, err)
			}
		})
	}
}
