package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestSemanticReadsUseIndex checks that every Phase 3 facet read searches an
// index constraining all of its key columns, never a table scan or a
// session-prefix-only search (P3-39, gate: EXPLAIN searches the full
// filter/order keys).
func TestSemanticReadsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	page := store.Page{Limit: 5}
	viewer := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	reads := []struct {
		name string
		keys []string
		read func(r store.SemanticReader) error
	}{
		{"CoverageMembers", []string{"session_id", "id"}, func(r store.SemanticReader) error { _, err := r.CoverageMembers("c", page); return err }},
		{"CoveragesBySource", []string{"session_id", "item_id", "purpose"}, func(r store.SemanticReader) error {
			_, err := r.CoveragesBySource("i", domain.CoverageProvenance, page)
			return err
		}},
		{"ExchangesByConversation", []string{"session_id", "f_conversation_id"}, func(r store.SemanticReader) error {
			_, err := r.ExchangesByConversation("conv", page)
			return err
		}},
		{"ExchangeMembers", []string{"session_id", "f_exchange_id"}, func(r store.SemanticReader) error { _, err := r.ExchangeMembers("x", page); return err }},
		{"MembershipsByItem", []string{"session_id", "f_source_item_id"}, func(r store.SemanticReader) error { _, err := r.MembershipsByItem("i", page); return err }},
		{"AdmissionsByExchange", []string{"session_id", "f_exchange_id"}, func(r store.SemanticReader) error {
			_, err := r.AdmissionsByExchange("x", page)
			return err
		}},
		{"OpenExchangesByTask", []string{"session_id", "f_principal_task_id"}, func(r store.SemanticReader) error {
			_, err := r.OpenExchangesByTask("task", page)
			return err
		}},
		{"ReservingCallsByTask", []string{"session_id", "f_principal_task_id"}, func(r store.SemanticReader) error {
			_, err := r.ReservingCallsByTask("task", page)
			return err
		}},
		{"CheckpointsByConversation", []string{"session_id", "f_conversation_id"}, func(r store.SemanticReader) error {
			_, err := r.CheckpointsByConversation(viewer, "conv", page)
			return err
		}},
		{"GrantsFor", []string{"session_id", "action", "target_key"}, func(r store.SemanticReader) error {
			_, err := r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i"), 5)
			return err
		}},
		{"LiveGrantsFor", []string{"session_id", "action", "target_key"}, func(r store.SemanticReader) error {
			_, err := r.LiveGrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i"), 9, 5)
			return err
		}},
		{"LifecycleByTarget", []string{"session_id", "f_target_kind", "f_target_id"}, func(r store.SemanticReader) error {
			_, err := r.LifecycleByTarget(domain.TargetItem, "i", page)
			return err
		}},
		{"SemanticChanges", []string{"session_id", "f_target_authorization_key"}, func(r store.SemanticReader) error {
			_, err := r.SemanticChanges(viewer, domain.ItemGrantTarget("s", "i"), page)
			return err
		}},
		{"ResourceUpdates", []string{"session_id", "f_resource_id"}, func(r store.SemanticReader) error { _, err := r.ResourceUpdates("repo", page); return err }},
		{"ResourceUpdatesAffectingPath", []string{"session_id", "resource_id"}, func(r store.SemanticReader) error {
			_, err := r.ResourceUpdatesAffectingPath("repo", "src/a.go", page)
			return err
		}},
		{"WorkspaceBindingsByContext", []string{"session_id", "f_context_kind", "f_context_id"}, func(r store.SemanticReader) error {
			_, err := r.WorkspaceBindingsByContext("", "task", "", page)
			return err
		}},
		{"RunsBySubject", []string{"session_id", "f_subject_key"}, func(r store.SemanticReader) error { _, err := r.RunsBySubject("sub", page); return err }},
		{"ObservationsByRun", []string{"session_id", "f_run_id"}, func(r store.SemanticReader) error { _, err := r.ObservationsByRun("run", page); return err }},
		{"SubjectStatesByResource", []string{"session_id", "f_resource"}, func(r store.SemanticReader) error {
			_, err := r.SubjectStatesByResource("repo", page)
			return err
		}},
		{"ProofDependencies", []string{"session_id", "f_proof_id"}, func(r store.SemanticReader) error { _, err := r.ProofDependencies("p", page); return err }},
		{"TransitionsByVersion", []string{"session_id", "f_obligation_id", "f_version"}, func(r store.SemanticReader) error {
			_, err := r.TransitionsByVersion(domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}, page)
			return err
		}},
		{"ObligationsByTaskOwner", []string{"session_id", "f_access_task_id"}, func(r store.SemanticReader) error {
			_, err := r.ObligationsByTaskOwner("task", page)
			return err
		}},
		{"CurrentBoundObligationsBySubject", []string{"session_id", "f_target_subject_key"}, func(r store.SemanticReader) error {
			_, err := r.CurrentBoundObligationsBySubject("sub", page)
			return err
		}},
		{"CurrentProofsByDependency(path)", []string{"session_id", "resource_id"}, func(r store.SemanticReader) error {
			_, err := r.CurrentProofsByDependency("repo", "k", page)
			return err
		}},
		{"CurrentProofsByDependency(all)", []string{"session_id", "resource_id"}, func(r store.SemanticReader) error {
			_, err := r.CurrentProofsByDependency("repo", "", page)
			return err
		}},
		{"LeasesByHolder", []string{"session_id", "f_conversation_id", "f_turn_id", "f_holder_task_id", "f_holder_agent_id"}, func(r store.SemanticReader) error {
			_, err := r.LeasesByHolder(domain.Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: domain.AuthorityAgent}, "conv", "turn", page)
			return err
		}},
		{"LeasesBySource", []string{"session_id", "f_source_item_id", "f_source_content_hash"}, func(r store.SemanticReader) error {
			_, err := r.LeasesBySource(domain.ItemContentRef{ItemID: "i", ContentHash: "h"}, page)
			return err
		}},
		{"RetrievalEventsByRequest", []string{"session_id", "f_request_id"}, func(r store.SemanticReader) error {
			_, err := r.RetrievalEventsByRequest(viewer, "req", page)
			return err
		}},
		{"OpenGoalsByTaskOwner", []string{"session_id", "f_task_id"}, func(r store.SemanticReader) error {
			_, err := r.OpenGoalsByTaskOwner("task", page)
			return err
		}},
		{"GCCandidates(task)", []string{"session_id", "f_task_id"}, func(r store.SemanticReader) error {
			_, err := r.GCCandidates(store.GCCandidateFilter{Viewer: viewer, Scope: domain.CollectTask, TaskID: "task", SnapshotSeq: 9, Page: page})
			return err
		}},
		{"PendingGCRequests", []string{"session_id"}, func(r store.SemanticReader) error { _, err := r.PendingGCRequests(page); return err }},
	}
	for _, rd := range reads {
		var q string
		if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if err := rd.read(r); err != nil {
				return err
			}
			q = tx.(*transaction).lastQuery
			return nil
		}); err != nil {
			t.Fatalf("%s: %v", rd.name, err)
		}
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = "x"
		}
		assertIndexed(t, s, rd.keys, q, args...)
	}
	// Single-record lookups by a secondary key.
	for _, c := range []struct {
		keys []string
		q    string
	}{
		{[]string{"session_id", "f_kind", "f_owner_id"}, "SELECT id FROM rec_owner WHERE session_id=? AND f_kind=? AND f_owner_id=?"},
		{[]string{"session_id", "f_exchange_id", "f_position"}, "SELECT id FROM rec_exchange_member WHERE session_id=? AND f_exchange_id=? AND f_position=?"},
		{[]string{"session_id", "f_exchange_id"}, "SELECT id FROM rec_exchange_ack WHERE session_id=? AND f_exchange_id=?"},
		{[]string{"session_id", "f_item_id"}, "SELECT id FROM rec_checkpoint WHERE session_id=? AND f_item_id=?"},
		{[]string{"session_id", "f_family", "f_request_id"}, "SELECT id FROM rec_mutation_receipt WHERE session_id=? AND f_family=? AND f_request_id=?"},
		{[]string{"session_id", "f_conversation_id"}, "SELECT COALESCE(MAX(f_ordinal),0) FROM rec_exchange WHERE session_id=? AND f_conversation_id=?"},
		{[]string{"session_id", "f_id"}, "SELECT id FROM rec_creation_declaration WHERE session_id=? AND f_id=?"},
		{[]string{"session_id", "f_id"}, "SELECT id FROM rec_resource_binding WHERE session_id=? AND f_id=?"},
		{[]string{"session_id", "f_resource_id", "f_request_id"}, "SELECT id FROM rec_resource_update WHERE session_id=? AND f_resource_id=? AND f_request_id=?"},
		{[]string{"session_id", "f_state_semantic_meta_id"}, "SELECT id FROM rec_subject_state WHERE session_id=? AND f_state_semantic_meta_id=?"},
		{[]string{"session_id", "f_item_id"}, "SELECT id FROM rec_projection WHERE session_id=? AND f_item_id=?"},
		{[]string{"session_id", "f_subject_key", "f_ordinal"}, "SELECT id FROM rec_observation_run WHERE session_id=? AND f_subject_key=? AND f_ordinal=?"},
		{[]string{"session_id", "f_run_id"}, "SELECT id FROM rec_observation WHERE session_id=? AND f_run_id=? AND " + closingObservation},
		{[]string{"session_id", "f_collect_intent_request_id"}, "SELECT id FROM rec_gc_request WHERE session_id=? AND f_collect_intent_request_id=?"},
		{[]string{"session_id", "f_declaration_semantic_meta_id"}, "SELECT id FROM rec_obligation_declaration WHERE session_id=? AND f_declaration_semantic_meta_id=?"},
		{[]string{"session_id", "f_type", "f_to_id"}, "SELECT 1 FROM rec_relationship WHERE session_id=? AND f_type=? AND f_to_id=? LIMIT 1"},
		{[]string{"session_id", "f_type", "f_from_id"}, "SELECT 1 FROM rec_relationship WHERE session_id=? AND f_type=? AND f_from_id=? LIMIT 1"},
	} {
		args := make([]any, strings.Count(c.q, "?"))
		for i := range args {
			args[i] = "x"
		}
		assertIndexed(t, s, c.keys, c.q, args...)
	}
}

// TestLiveReadsUseLiveIndexes checks the G2 live-only reads search their
// live indexes, so dead history is never visited row by row: CURRENT
// subject states through 0032's partial index, and unrevoked grants
// through 0031's liveness index.
func TestLiveReadsUseLiveIndexes(t *testing.T) {
	s, _ := openTemp(t)
	for _, c := range []struct {
		index string
		read  func(r store.SemanticReader) error
	}{
		{"subject_state_resource_current", func(r store.SemanticReader) error {
			_, err := r.SubjectStatesByResource("repo", store.Page{Limit: 5})
			return err
		}},
		{"lookup_grant_target_live", func(r store.SemanticReader) error {
			_, err := r.LiveGrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "i"), 9, 5)
			return err
		}},
	} {
		var q string
		if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if err := c.read(r); err != nil {
				return err
			}
			q = tx.(*transaction).lastQuery
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = 1
		}
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if !strings.Contains(strings.Join(plan, "\n"), c.index) {
			t.Errorf("%q does not use %s\nplan: %v", q, c.index, plan)
		}
	}
}

// TestH2LatestReadsAreKeyed checks the H2 exact-key reads: each searches
// an index on its full key and never sorts, so a newest-first LIMIT 1 or a
// keyed lookup costs the same however long the history is.
func TestH2LatestReadsAreKeyed(t *testing.T) {
	s, _ := openTemp(t)
	for _, c := range []struct {
		keys []string
		q    string
	}{
		{[]string{"session_id", "resource_id", "path_key"}, "SELECT seq, update_id FROM lookup_resource_update_path WHERE session_id=? AND resource_id=? AND path_key=? ORDER BY seq DESC, update_id DESC LIMIT 1"},
	} {
		args := make([]any, strings.Count(c.q, "?"))
		for i := range args {
			args[i] = "x"
		}
		assertIndexed(t, s, c.keys, c.q, args...)
		assertNoSort(t, s, c.q, args...)
	}
}

// assertNoSort fails when q's plan builds a temporary B-tree: a sort or
// DISTINCT over every matching row.
func assertNoSort(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Errorf("query sorts every matching row: %q\nstep: %s", q, detail)
		}
	}
}
