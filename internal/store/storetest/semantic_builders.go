package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Meta returns the companion identity every Phase 3 record embeds.
func Meta(sess, id string, seq uint64) domain.SemanticMeta {
	return domain.SemanticMeta{ID: id, SessionID: sess, SchemaVersion: domain.SemanticSchemaV1, Seq: seq}
}

// AgentPrincipal is the AGENT that owns conversation ConversationIDFor(task,
// agent) in the builders below.
func AgentPrincipal(sess, task, agent string) domain.Principal {
	return domain.Principal{SessionID: sess, WorkflowID: "wf", TaskID: task, AgentID: agent, Authority: domain.AuthorityAgent}
}

// HarnessPrincipal is a trusted HARNESS actor for acknowledgments and
// registrations.
func HarnessPrincipal(sess string) domain.Principal {
	return domain.Principal{SessionID: sess, Authority: domain.AuthorityHarness}
}

// NewExchange returns an OPEN logical exchange of (task, agent)'s
// conversation at ordinal.
func NewExchange(sess, id, task, agent string, ordinal, seq uint64) domain.LogicalExchange {
	return domain.LogicalExchange{
		SemanticMeta:   Meta(sess, id, seq),
		ConversationID: domain.ConversationIDFor(task, agent),
		Ordinal:        ordinal,
		Principal:      AgentPrincipal(sess, task, agent),
		TurnID:         "turn-1",
		Turn:           1,
		State:          domain.ExchangeOpen,
		Revision:       1,
	}
}

// ContentRef names the item's exact immutable content.
func ContentRef(it domain.ContextItem) domain.ItemContentRef {
	return domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}
}

// NewCoverage returns a coverage record and its members, sorted by member
// key and signed, so InsertCoverage accepts them. Each member is a source
// content reference.
func NewCoverage(t *testing.T, sess, id string, seq uint64, purpose domain.CoveragePurpose, sources ...domain.ItemContentRef) (domain.CoverageRecord, []domain.CoverageMember) {
	t.Helper()
	members := make([]domain.CoverageMember, len(sources))
	for i := range sources {
		src := sources[i]
		members[i] = domain.CoverageMember{SemanticMeta: Meta(sess, "", seq), CoverageID: id, Source: &src}
	}
	return signCoverage(t, domain.CoverageRecord{
		SemanticMeta: Meta(sess, id, seq),
		Purpose:      purpose,
		Access:       domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess},
	}, members)
}

// signCoverage sets each member's ID to its canonical key (the store keys
// members by (CoverageID, key) and requires ID == key), sorts them, and
// signs c.
func signCoverage(t *testing.T, c domain.CoverageRecord, members []domain.CoverageMember) (domain.CoverageRecord, []domain.CoverageMember) {
	t.Helper()
	for i := range members {
		key, err := members[i].Key()
		if err != nil {
			t.Fatal(err)
		}
		members[i].ID = key
	}
	sortMembers(t, members)
	c.MemberCount = uint64(len(members))
	sig, err := domain.CoverageSignature(c, members)
	if err != nil {
		t.Fatal(err)
	}
	c.Signature = sig
	return c, members
}

func sortMembers(t *testing.T, members []domain.CoverageMember) {
	t.Helper()
	key := func(m domain.CoverageMember) string {
		k, err := m.Key()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	for i := 1; i < len(members); i++ {
		for j := i; j > 0 && key(members[j]) < key(members[j-1]); j-- {
			members[j], members[j-1] = members[j-1], members[j]
		}
	}
}

// semantic returns tx's Phase 3 facet or fails the test.
func semantic(t *testing.T, tx store.Tx) store.SemanticTx {
	t.Helper()
	s, err := store.Semantic(tx)
	if err != nil {
		t.Fatalf("store.Semantic: %v", err)
	}
	return s
}

// readSemantic returns tx's Phase 3 read facet or fails the test.
func readSemantic(t *testing.T, tx store.ReadTx) store.SemanticReader {
	t.Helper()
	s, err := store.ReadSemantic(tx)
	if err != nil {
		t.Fatalf("store.ReadSemantic: %v", err)
	}
	return s
}
