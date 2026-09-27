package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// evidenceTx injects the provenance records evidence qualification reads —
// projections, exchange memberships and ingest receipts — over a real store,
// so each SEC-1.3 vector is exercised without building its full producer.
type evidenceTx struct {
	store.Tx
	reader   *evidenceReader
	receipts map[string]domain.IngestReceipt
}

func (t evidenceTx) SemanticReadBackend() store.SemanticReader      { return t.reader }
func (t evidenceTx) SemanticTransaction() (store.SemanticTx, error) { return store.Semantic(t.Tx) }
func (t evidenceTx) Receipt(occurrence string) (domain.IngestReceipt, error) {
	if r, ok := t.receipts[occurrence]; ok {
		return r, nil
	}
	return t.Tx.Receipt(occurrence)
}

type evidenceReader struct {
	store.SemanticReader
	projections map[string]domain.ProjectionRecord
	members     map[string][]domain.ExchangeMember
}

func (r *evidenceReader) ProjectionByItem(id string) (domain.ProjectionRecord, error) {
	if p, ok := r.projections[id]; ok {
		return p, nil
	}
	return domain.ProjectionRecord{}, domain.ErrNotFound
}
func (r *evidenceReader) MembershipsByItem(id string, _ store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	return store.ResultPage[domain.ExchangeMember]{Records: r.members[id]}, nil
}

type evidenceFixture struct {
	t      *testing.T
	tx     store.Tx
	wrap   evidenceTx
	sess   string
	nextID int
}

func (f *evidenceFixture) item(role domain.ItemRole, authority domain.Authority, kind domain.Kind) domain.ContextItem {
	f.nextID++
	id := "src-" + string(rune('a'+f.nextID))
	it := storetest.NewItem(f.sess, id, f.tx.NextSeq(), "content "+id)
	it.Role, it.Authority, it.Kind = role, authority, kind
	it.Generation, it.Retention = domain.GenerationWorking, domain.RetentionNormal
	it.Scope = domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: f.sess, TaskID: it.TaskID}
	if kind != domain.KindGoal {
		it.GoalStatus = nil
	}
	return it
}

func (f *evidenceFixture) registerToolResult(it domain.ContextItem, hash string) {
	f.wrap.reader.members[it.ID] = append(f.wrap.reader.members[it.ID], domain.ExchangeMember{ExchangeID: "x", Position: 3, Role: domain.MemberToolResult, Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: hash}, CallID: "call", ToolCallID: "toolu"})
}

func (f *evidenceFixture) ingestedBy(it *domain.ContextItem, authority domain.Authority, eventID string) {
	it.EventID = eventID
	f.wrap.receipts[domain.CallerOccurrenceID(f.sess, eventID)] = domain.IngestReceipt{SessionID: f.sess, EventID: eventID, Principal: principal(f.sess, authority), Items: []domain.ContextItem{*it}}
}

// projectionOf stores src (the returned projection is inserted by the test)
// and a projection record naming its exact content.
func (f *evidenceFixture) projectionOf(src domain.ContextItem, kind domain.Kind) domain.ContextItem {
	mustInsert(f.t, f.tx, src)
	p := f.item(domain.RoleProjection, domain.AuthorityTool, kind)
	f.wrap.reader.projections[p.ID] = domain.ProjectionRecord{ItemID: p.ID, SemanticMeta: domain.SemanticMeta{SessionID: f.sess}, Source: domain.ItemContentRef{ItemID: src.ID, ContentHash: src.ContentHash}}
	return p
}

// TestEvidenceSupportRequiresTrustedEvidenceProvenance is SEC-1.3: citing an
// item as evidence SUPPORT requires an evidence-category kind, non-AGENT
// provenance, and for a TOOL tool_result transcript a trusted dispatcher's
// TOOL_RESULT registration or HARNESS/SYSTEM ingestion. A projection
// qualifies only through its source. Vectors (a)-(f) are the review's.
func TestEvidenceSupportRequiresTrustedEvidenceProvenance(t *testing.T) {
	type build func(f *evidenceFixture) domain.ContextItem
	cases := []struct {
		name string
		want bool
		make build
		// declarationOnly marks a projection case: EVIDENCE_SUPPORT coverage
		// additionally verifies the projection's lease chain, which this
		// double does not model, so only the declaration path can accept it.
		declarationOnly bool
	}{
		{name: "registered external tool result", want: true, make: func(f *evidenceFixture) domain.ContextItem {
			it := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.registerToolResult(it, it.ContentHash)
			return it
		}},
		{name: "tool result ingested by HARNESS", want: true, make: func(f *evidenceFixture) domain.ContextItem {
			it := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.ingestedBy(&it, domain.AuthorityHarness, "harness-tool-1")
			return it
		}},
		{name: "user-supplied evidence", want: true, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleSemantic, domain.AuthorityUser, domain.KindEvidence)
		}},
		{name: "projection of a registered tool result", want: true, make: func(f *evidenceFixture) domain.ContextItem {
			src := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.registerToolResult(src, src.ContentHash)
			return f.projectionOf(src, domain.KindToolResult)
		}, declarationOnly: true},
		{name: "(a) agent's own unsupported fact", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleSemantic, domain.AuthorityAgent, domain.KindFact)
		}},
		{name: "(b) agent completion claim of kind evidence", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleSemantic, domain.AuthorityAgent, domain.KindEvidence)
		}},
		{name: "(c) TOOL projection of the agent's fact", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.projectionOf(f.item(domain.RoleSemantic, domain.AuthorityAgent, domain.KindFact), domain.KindFact)
		}},
		{name: "(c) evidence-kind projection of an agent claim", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.projectionOf(f.item(domain.RoleSemantic, domain.AuthorityAgent, domain.KindEvidence), domain.KindEvidence)
		}},
		{name: "(d) projection of the tool's acknowledgment", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			ack := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindConversation)
			f.registerToolResult(ack, ack.ContentHash)
			return f.projectionOf(ack, domain.KindToolResult)
		}},
		{name: "(e) projection of the agent's output transcript", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.projectionOf(f.item(domain.RoleTranscript, domain.AuthorityAgent, domain.KindAssistantMessage), domain.KindToolResult)
		}},
		{name: "(f) TOOL event plain-ingested by an AGENT", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			it := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.ingestedBy(&it, domain.AuthorityAgent, "fake-tool-1")
			return it
		}},
		{name: "tool result without provenance", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
		}},
		{name: "tool result member for other content", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			it := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.registerToolResult(it, domain.HashBytes([]byte("other")))
			return it
		}},
		{name: "receipt that does not contain the item", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			it := f.item(domain.RoleTranscript, domain.AuthorityTool, domain.KindToolResult)
			f.ingestedBy(&it, domain.AuthorityHarness, "harness-tool-2")
			r := f.wrap.receipts[domain.CallerOccurrenceID(f.sess, "harness-tool-2")]
			r.Items = nil
			f.wrap.receipts[domain.CallerOccurrenceID(f.sess, "harness-tool-2")] = r
			return it
		}},
		{name: "retrieved content", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleSemantic, domain.AuthorityRetrievedContent, domain.KindEvidence)
		}},
		{name: "projection without a projection record", want: false, make: func(f *evidenceFixture) domain.ContextItem {
			return f.item(domain.RoleProjection, domain.AuthorityTool, domain.KindToolResult)
		}},
	}
	for _, tc := range cases {
		for _, path := range []string{"coverage", "declaration"} {
			if tc.declarationOnly && path == "coverage" {
				continue
			}
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				eachStore(t, func(t *testing.T, s store.Store) {
					const sess = "sess-sec13"
					err := s.Update(ctx, sess, func(tx store.Tx) error {
						f := &evidenceFixture{t: t, tx: tx, sess: sess, wrap: evidenceTx{Tx: tx, reader: &evidenceReader{projections: map[string]domain.ProjectionRecord{}, members: map[string][]domain.ExchangeMember{}}, receipts: map[string]domain.IngestReceipt{}}}
						r, err := store.ReadSemantic(tx)
						if err != nil {
							return err
						}
						f.wrap.reader.SemanticReader = r
						src := tc.make(f)
						mustInsert(t, tx, src)
						if path == "coverage" {
							derived := taskItem(sess, "fact", tx.NextSeq(), domain.AuthorityUser)
							mustInsert(t, tx, derived)
							_, err = LinkDerivedCoverage(f.wrap, principal(sess, domain.AuthorityUser), derived.ID, []string{src.ID}, domain.CoverageEvidenceSupport, "evt", 4)
							return err
						}
						keyed := agentDirective(sess, "keyed", domain.AgentKeyID("status"), tx.NextSeq())
						mustInsert(t, tx, keyed)
						_, err = DeclareCreation(f.wrap, keyed, CreationAcceptance{PolicyVersion: testDeclarationPolicy, SupportIDs: []string{src.ID}})
						return err
					})
					if tc.want && err != nil {
						t.Fatalf("qualifying evidence refused: %v", err)
					}
					if !tc.want && !errors.Is(err, domain.ErrInvalidRecord) && !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
						t.Fatalf("err = %v; forged evidence must be refused", err)
					}
				})
			})
		}
	}
}
