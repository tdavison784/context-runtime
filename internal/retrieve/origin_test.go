package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type originReader struct {
	store.MembershipReader
	exchange domain.LogicalExchange
	members  []domain.ExchangeMember
}

func (r originReader) LogicalExchange(string) (domain.LogicalExchange, error) { return r.exchange, nil }
func (r originReader) ExchangeMembers(string, store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	return store.ResultPage[domain.ExchangeMember]{Records: r.members}, nil
}

func TestToolOriginRequiresCompletedOutputAndExactToolCall(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	conv := domain.ConversationIDFor(p.TaskID, p.AgentID)
	inv := domain.ToolInvocation{SessionID: "s", ConversationID: conv, CallID: "call", ToolCallID: "tool", ExchangeID: "exchange", TurnID: "turn", Principal: p}
	origin := domain.RetrievalOrigin{Holder: p, ConversationID: conv, TurnID: "turn", Invocation: &inv}
	meta := domain.SemanticMeta{SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 2}
	ref := domain.ItemContentRef{ItemID: "output", ContentHash: domain.HashBytes([]byte("output"))}
	out := domain.ExchangeMember{SemanticMeta: meta, ExchangeID: "exchange", Position: 1, Role: domain.MemberOutput, Source: ref, CallID: "call"}
	out.ID = "output-member"
	tool := domain.ExchangeMember{SemanticMeta: meta, ExchangeID: "exchange", Position: 2, Role: domain.MemberToolCall, Source: ref, CallID: "call", ToolCallID: "tool"}
	tool.ID = "tool-member"
	r := originReader{exchange: domain.LogicalExchange{SemanticMeta: domain.SemanticMeta{ID: "exchange", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 1}, ConversationID: conv, Principal: p, TurnID: "turn", Turn: 1, State: domain.ExchangeExecuting, Ordinal: 1, Revision: 1}, members: []domain.ExchangeMember{out, tool}}
	if err := validateToolOrigin(r, origin, 8, 8); err != nil {
		t.Fatalf("authenticated origin = %v", err)
	}
	r.members[1].ToolCallID = "other"
	if err := validateToolOrigin(r, origin, 8, 8); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("unrelated tool call = %v", err)
	}
	r.members = r.members[1:]
	r.members[0].ToolCallID, r.members[0].Position = "tool", 1
	if err := validateToolOrigin(r, origin, 8, 8); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("missing completed output = %v", err)
	}
}

func TestToolOriginRejectsDuplicateOutputAndCallBeforeOutput(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	conv := domain.ConversationIDFor(p.TaskID, p.AgentID)
	inv := domain.ToolInvocation{SessionID: "s", ConversationID: conv, CallID: "call", ToolCallID: "tool", ExchangeID: "exchange", TurnID: "turn", Principal: p}
	origin := domain.RetrievalOrigin{Holder: p, ConversationID: conv, TurnID: "turn", Invocation: &inv}
	meta := domain.SemanticMeta{SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 2}
	ref := domain.ItemContentRef{ItemID: "output", ContentHash: domain.HashBytes([]byte("output"))}
	out := domain.ExchangeMember{SemanticMeta: meta, ExchangeID: "exchange", Position: 1, Role: domain.MemberOutput, Source: ref, CallID: "call"}
	out.ID = "output-member"
	tool := domain.ExchangeMember{SemanticMeta: meta, ExchangeID: "exchange", Position: 2, Role: domain.MemberToolCall, Source: ref, CallID: "call", ToolCallID: "tool"}
	tool.ID = "tool-member"
	r := originReader{exchange: domain.LogicalExchange{SemanticMeta: domain.SemanticMeta{ID: "exchange", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 1}, ConversationID: conv, Principal: p, TurnID: "turn", Turn: 1, State: domain.ExchangeExecuting, Ordinal: 1, Revision: 1}, members: []domain.ExchangeMember{out, tool}}
	r.members[0].Position, r.members[1].Position = 3, 2
	if err := validateToolOrigin(r, origin, 8, 8); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("call before output accepted: %v", err)
	}
	r.members[0].Position, r.members[1].Position = 1, 2
	extra := out
	extra.ID, extra.Position = "extra-output", 3
	r.members = append(r.members, extra)
	if err := validateToolOrigin(r, origin, 8, 8); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("duplicate output accepted: %v", err)
	}
}
