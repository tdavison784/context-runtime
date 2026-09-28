package retrieve

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_30_APrivateResultNeverWidensToTask closes the P3-42 table row
// "A-private result never TASK-wide" (ADR8:1236): the cited test was about
// denied-audit redaction, not the delivered boundary. On both stores a
// rehydrated AGENT-scoped source produces a projection, coverage and result
// whose access is still the agent-private intersection — never the TASK
// boundary the conversation runs under — and a different agent in the same
// task cannot read the projection, while the holder can.
func TestP3_30_APrivateResultNeverWidensToTask(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bOpen29() {
		t.Run(name, func(t *testing.T) {
			s, _ := open(t)
			holder := storetest.NewPrincipal("s", domain.AuthorityHarness)
			conv := storetest.NewConversation("s", domain.ConversationIDFor(holder.TaskID, "agent"))
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				if _, err := tx.PutTask(storetest.NewTask("s", holder.TaskID), 0, domain.LifecycleEvent{ID: "p342b30-task", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: holder.TaskID, Action: "open", Actor: holder}); err != nil {
					return err
				}
				if _, err := tx.PutConversation(conv, 0); err != nil {
					return err
				}
				source := storetest.NewItem("s", "source", tx.NextSeq(), "agent-private history")
				source.Scope, source.AgentID = domain.ScopeAgent, "agent"
				source.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", TaskID: holder.TaskID, AgentID: "agent"}
				source.Residency = domain.ResidencyArchived
				return tx.InsertItem(source)
			}); err != nil {
				t.Fatal(err)
			}
			intent := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "r36", ItemID: "source"},
				Origin: domain.RetrievalOrigin{Holder: holder, ConversationID: conv.ConversationID, TurnID: "turn-1"}, Method: "rehydrate"}
			out, err := New(s).Rehydrate(ctx, holder, intent, leasePolicy(), false)
			if err != nil {
				t.Fatal(err)
			}
			var itemID string
			sourceBoundary := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", TaskID: holder.TaskID, AgentID: "agent"}
			foreign := holder
			foreign.AgentID = "other-agent"
			p342bSem(t, s, func(sem store.SemanticReader) error {
				projection, err := sem.Projection(out.ProjectionID)
				if err != nil {
					return err
				}
				itemID = projection.ItemID
				result, err := sem.RetrievalResult(out.ID)
				if err != nil {
					return err
				}
				coverage, err := sem.Coverage(projection.DependencyCoverageID)
				if err != nil {
					return err
				}
				// The boundary is constraint-driven (Permits matches on
				// workflow/task/agent owners, FR-DOM-003), so "never TASK-wide"
				// means: the agent constraint survives the intersection, the
				// result stays within the source boundary, and a task-mate
				// agent is still refused.
				for name, got := range map[string]domain.AccessBoundary{"projection": projection.Access, "result": result.Access, "coverage": coverage.Access} {
					if got.AgentID != "agent" || got.TaskID != holder.TaskID || got.SessionID != "s" {
						t.Fatalf("%s lost the agent constraint: %+v", name, got)
					}
					if !got.Within(sourceBoundary) {
						t.Fatalf("%s escaped the source boundary: %+v", name, got)
					}
					if got.Permits(foreign) {
						t.Fatalf("%s is task-wide: %+v permits %+v", name, got, foreign)
					}
				}
				return nil
			})
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				it, err := tx.Item(itemID)
				if err != nil || it.Access.AgentID != "agent" || it.Access.Permits(foreign) {
					t.Fatalf("projection item access widened: %+v %v", it.Access, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// The same task's other agent is refused; the holder reads it.
			other := holder
			other.AgentID = "other-agent"
			if _, err := New(s).Get(ctx, other, itemID); !errors.Is(err, domain.ErrNotFound) || err.Error() != domain.ErrNotFound.Error() {
				t.Fatalf("foreign agent read the private projection: %v", err)
			}
			if _, err := New(s).Get(ctx, holder, itemID); err != nil {
				t.Fatalf("holder cannot read its own projection: %v", err)
			}
		})
	}
}

// p342bInheritedFixture is the copied-representation world of
// TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage, on which
// the lease/nested-member drops below are performed.
func p342bInheritedFixture(t *testing.T) DependencySnapshot {
	t.Helper()
	d := dependencyFixture(t)
	projected := storetest.NewItem("s", d.Projection.ItemID, 6, "copied history")
	projected.Role, projected.Kind, projected.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	projected.Scope, projected.Access = domain.ScopeAgent, d.Projection.Access
	projected.Source = &domain.SourceRef{Kind: domain.SourceItem, Locator: d.Projection.Source.ItemID, ContentHash: d.Projection.Source.ContentHash}
	derived := storetest.NewItem("s", "derived", 7, "summary")
	derived.Scope, derived.Access = domain.ScopeAgent, d.Projection.Access
	root := domain.CoverageRecord{SemanticMeta: domain.SemanticMeta{ID: "representation", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1},
		Purpose: domain.CoverageRepresentation, Access: derived.Access, MemberCount: 3}
	projectedRef := domain.ItemContentRef{ItemID: projected.ID, ContentHash: projected.ContentHash}
	originalRef := d.Projection.Source
	members := []domain.CoverageMember{
		{SemanticMeta: domain.SemanticMeta{ID: "projected-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, Source: &projectedRef},
		{SemanticMeta: domain.SemanticMeta{ID: "lease-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, Source: &originalRef, LeaseID: d.Projection.LeaseID},
		{SemanticMeta: domain.SemanticMeta{ID: "nested-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, NestedCoverageID: d.Projection.DependencyCoverageID},
	}
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { x, _ := a.Key(); y, _ := b.Key(); return cmp.Compare(x, y) })
	root.Signature, _ = domain.CoverageSignature(root, members)
	d.Coverages[root.ID], d.Members[root.ID] = root, members
	d.Sources[projected.ID] = projected
	d.Projections = map[string]domain.ProjectionRecord{projected.ID: d.Projection}
	d.Derived, d.RootCoverageID, d.SnapshotSeq = derived, root.ID, 8
	d.Link = storetest.NewRelationship("s", "derived-link", domain.RelDerivedFrom, derived.ID, projected.ID, 8)
	d.Link.CoverageID = root.ID
	return d
}

// resign recomputes the root coverage over whatever members remain.
func p342bResign(root *domain.CoverageRecord, members []domain.CoverageMember) {
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { x, _ := a.Key(); y, _ := b.Key(); return cmp.Compare(x, y) })
	root.MemberCount = uint64(len(members))
	root.Signature, _ = domain.CoverageSignature(*root, members)
}

// TestP3_30_CopiedRepresentationCannotDropLeaseOrNestedMember closes the
// P3-42 table row "copied provider representation cannot drop dependency"
// (ADR8:1238). The cited test deleted the projection RECORD; the lease and
// the nested member were never dropped. Each drop is rejected with
// ErrIncompleteCoverage: the lease record behind the copied pair, the
// source+lease member itself, the nested-coverage pointer, and the nested
// coverage record. Pure checker test: CheckRepresentationDependencies takes
// the snapshot, not a store (the store-backed halves of P3-30 are the 1236
// and 1241 tests).
func TestP3_30_CopiedRepresentationCannotDropLeaseOrNestedMember(t *testing.T) {
	if err := CheckRepresentationDependencies(p342bInheritedFixture(t)); err != nil {
		t.Fatalf("control rejected: %v", err)
	}
	d := p342bInheritedFixture(t)
	delete(d.Leases, d.Projection.LeaseID)
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("dropped lease record: %v", err)
	}
	d = p342bInheritedFixture(t)
	kept := d.Members[d.RootCoverageID][:0]
	for _, m := range d.Members[d.RootCoverageID] {
		if m.LeaseID == "" {
			kept = append(kept, m)
		}
	}
	root := d.Coverages[d.RootCoverageID]
	p342bResign(&root, kept)
	d.Coverages[d.RootCoverageID], d.Members[d.RootCoverageID] = root, kept
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("dropped source+lease member: %v", err)
	}
	d = p342bInheritedFixture(t)
	kept = d.Members[d.RootCoverageID][:0]
	for _, m := range d.Members[d.RootCoverageID] {
		if m.NestedCoverageID == "" {
			kept = append(kept, m)
		}
	}
	root = d.Coverages[d.RootCoverageID]
	p342bResign(&root, kept)
	d.Coverages[d.RootCoverageID], d.Members[d.RootCoverageID] = root, kept
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("dropped nested-coverage member: %v", err)
	}
	d = p342bInheritedFixture(t)
	delete(d.Coverages, d.Projection.DependencyCoverageID)
	// The missing nested record surfaces as the bare ErrNotFound of the
	// record load rather than ErrIncompleteCoverage — still a closed refusal.
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dropped nested coverage record: %v", err)
	}
}

// TestP3_30_RetrievalRecordsRollBackAsOneUnit closes the P3-42 table row
// "event/result/lease/receipt rollback as one unit" (ADR8:1241). The cited
// test was a pure record builder; this drives the real Apply against both
// stores with storetest's per-write fault injection: failing ANY single
// write of the seven-record retrieval leaves no lease, receipt, result,
// event, projection item or coverage behind, and the sequence counter
// unchanged — all or nothing.
func TestP3_30_RetrievalRecordsRollBackAsOneUnit(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	newStore := func(t *testing.T) store.Store {
		s := memory.New()
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	sqliteStore := func(t *testing.T) store.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p342b30.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	for name, newStore := range map[string]func(t *testing.T) store.Store{"memory": newStore, "sqlite": sqliteStore} {
		t.Run(name, func(t *testing.T) {
			storetest.CheckAtomic(t, newStore, storetest.AtomicCase{
				Session: "s",
				Setup: func(t *testing.T, s store.Store) {
					seedLeaseStore(t, s)
				},
				Op: func(t *testing.T, tx store.Tx) error {
					_, err := Apply(tx, p, harnessIntent(p, "atomic-1"), leasePolicy(), false)
					return err
				},
				Snapshot: func(t *testing.T, tx store.ReadTx) any {
					type snap struct {
						lastSeq                  uint64
						leases                   int
						receipt, result, event   bool
						sourceVersion, itemFound int
					}
					var out snap
					out.lastSeq = tx.LastSeq()
					sem, err := store.ReadSemantic(tx)
					if err != nil {
						t.Fatal(err)
					}
					page, err := sem.LeasesByHolder(p, domain.ConversationIDFor(p.TaskID, p.AgentID), "turn-1", store.Page{Limit: 8})
					if err != nil {
						t.Fatal(err)
					}
					out.leases = len(page.Records)
					_, err = sem.MutationReceipt(domain.MutationRetrieval, "atomic-1")
					out.receipt = err == nil
					_, err = sem.RetrievalResult(retrievalID("result", "s", "atomic-1"))
					out.result = err == nil
					events, err := sem.RetrievalEventsByRequest(p, "atomic-1", store.Page{Limit: 8})
					if err != nil {
						t.Fatal(err)
					}
					out.event = len(events.Records) > 0
					if _, err := tx.Item(retrievalID("item", "s", "atomic-1")); err == nil {
						out.itemFound = 1
					}
					source, err := tx.Item("source")
					if err != nil {
						t.Fatal(err)
					}
					out.sourceVersion = int(source.Version)
					return out
				},
			})
		})
	}
}
