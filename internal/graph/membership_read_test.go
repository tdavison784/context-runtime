package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Read-only protocol double; backend persistence acceptance belongs to W2.
type membershipReadFixture struct {
	store.ReadTx
	store.SemanticReader
	state    domain.ConversationMembershipState
	xs       []domain.LogicalExchange
	ack      domain.ExchangeAcknowledgment
	manifest domain.AdmissionManifest
	call     domain.CallRecord
}

func (f *membershipReadFixture) SessionID() string                         { return "s" }
func (f *membershipReadFixture) SemanticReadBackend() store.SemanticReader { return f }
func (f *membershipReadFixture) LogicalExchange(id string) (domain.LogicalExchange, error) {
	for _, x := range f.xs {
		if x.ID == id {
			return x, nil
		}
	}
	return domain.LogicalExchange{}, domain.ErrNotFound
}
func (f *membershipReadFixture) ConversationMembership(string) (domain.ConversationMembershipState, error) {
	return f.state, nil
}
func (f *membershipReadFixture) ExchangesByConversation(_ string, p store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	if p.After.Seq == 0 {
		return store.ResultPage[domain.LogicalExchange]{Records: f.xs[:1], More: true, Next: store.Cursor{Seq: 1, ID: f.xs[0].ID}}, nil
	}
	return store.ResultPage[domain.LogicalExchange]{Records: f.xs[1:]}, nil
}
func (f *membershipReadFixture) ExchangeAcknowledgment(string) (domain.ExchangeAcknowledgment, error) {
	return f.ack, nil
}
func (f *membershipReadFixture) AdmissionManifest(string) (domain.AdmissionManifest, error) {
	return f.manifest, nil
}
func (f *membershipReadFixture) Call(string) (domain.CallRecord, error) { return f.call, nil }

func TestMembershipReadPrefixAuthenticatesBeforePublishing(t *testing.T) {
	state, xs := membershipPrefixFixture()
	x, ack, manifest, call := membershipAcknowledgmentFixture()
	xs[2].Ordinal = 2
	state.LastOrdinal, state.ClosedFrontier = 2, 1
	f := &membershipReadFixture{state: state, xs: []domain.LogicalExchange{x, xs[2]}, ack: ack, manifest: manifest, call: call}
	read := func(p domain.Principal, id string, limit int) (ClosedPrefixSnapshot, error) {
		return ReadClosedExchangePrefix(f, p, id, 1, limit)
	}
	if got, err := read(x.Principal, xs[2].ID, 7); err != nil || len(got.Exchanges) != 1 || got.Exchanges[0] != x {
		t.Fatalf("snapshot: %+v, %v", got, err)
	}
	if got, err := read(x.Principal, xs[2].ID, 6); err != domain.ErrResourceLimit || got.Exchanges != nil {
		t.Fatal("partial work accepted", err)
	}
	other := x.Principal
	other.AgentID = "b"
	if _, err := read(other, xs[2].ID, 7); err != domain.ErrNotFound {
		t.Fatal("private exchange disclosed", err)
	}
	if _, err := read(x.Principal, "missing", 7); err != domain.ErrNotFound {
		t.Fatal("missing differs from private", err)
	}
	f.ack.ConsumingCallID = "wrong-call"
	if got, err := read(x.Principal, xs[2].ID, 7); err != domain.ErrIncompleteCoverage || got.Exchanges != nil {
		t.Fatal("false acknowledgment accepted", err)
	}
}
