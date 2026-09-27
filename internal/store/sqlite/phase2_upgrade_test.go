package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// TestPhase2FixtureUpgrade is the P3-40/41 gate on real Phase 2 bytes: the
// database the merged Phase 2 binary wrote upgrades and reopens, reads back
// exactly the state that binary recorded (the only differences being the
// documented, audited upgrade effects), replays its receipts and envelopes
// under their recorded hash schema, leaves parsed commands unexecuted and
// claims unbound, invents no Phase 3 record, and serves the new indexed
// reads over its legacy rows.
func TestPhase2FixtureUpgrade(t *testing.T) {
	ctx := context.Background()
	path := sqlitetest.Phase2Path(t)
	m := sqlitetest.Phase2(t)
	for range 3 { // upgrade, then reopen twice: migrations apply once
		s, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatalf("open Phase 2 fixture: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sessions, err := s.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != len(m.Sessions) {
		t.Fatalf("sessions = %v, want %d", sessions, len(m.Sessions))
	}
	for sess, want := range m.Sessions {
		t.Run(sess, func(t *testing.T) {
			if err := s.View(ctx, sess, func(tx store.ReadTx) error {
				got := dumpPhase2(t, tx, m.Occurrences[sess], m.Viewers[sess])
				if sess == "s1" {
					checkFixtureCoverage(t, got)
				}
				want = expectUpgrade(t, want, got)
				assertJSONEqual(t, "upgraded state", got, want)
				checkReplay(t, got)
				checkNothingInvented(t, tx, got)
				checkIndexedReads(t, tx, sess, got)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The upgraded database accepts Phase 3 writes.
	if err := s.Update(ctx, "s1", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertOwnerRegistration(domain.OwnerRegistration{SemanticMeta: domain.SemanticMeta{ID: "owner-w", SessionID: "s1", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			Kind: domain.OwnerWorkflow, OwnerID: "W", SourceID: "upgrade-test", Actor: domain.Principal{SessionID: "s1", Authority: domain.AuthorityHarness}})
	}); err != nil {
		t.Fatalf("Phase 3 write after upgrade: %v", err)
	}
}

// checkFixtureCoverage keeps the comparison from passing vacuously: session
// s1 must hold the cases the fixture exists to exercise.
func checkFixtureCoverage(t *testing.T, got sqlitetest.Phase2Session) {
	t.Helper()
	reconciled, unknown := 0, 0
	for _, tr := range got.Transitions {
		if tr.Cause == domain.CauseUpgradeReconciliation {
			reconciled++
		}
	}
	for _, e := range got.Events {
		if e.RequestHashVersion == "unknown" {
			unknown++
		}
	}
	if reconciled != 1 || unknown != 1 || len(got.Commands) != 2 || len(got.Receipts) != 4 || len(got.Grants) != 2 || len(got.Calls) != 1 {
		t.Errorf("fixture coverage: %d reconciliations, %d unknown events, %d commands, %d receipts, %d grants, %d calls; want 1, 1, 2, 4, 2, 1",
			reconciled, unknown, len(got.Commands), len(got.Receipts), len(got.Grants), len(got.Calls))
	}
}

// expectUpgrade applies the documented upgrade effects to the recorded
// state: an envelope-less event record's request schema becomes unknown
// (migration 0018), and each legacy matcher-derived satisfaction is
// reconciled (migration 0026), which adds one SYSTEM transition at the next
// sequence and returns the version to UNRESOLVED.
func expectUpgrade(t *testing.T, want, got sqlitetest.Phase2Session) sqlitetest.Phase2Session {
	t.Helper()
	receipted := map[string]bool{}
	for _, env := range want.Envelopes {
		receipted[env.EventID] = true
	}
	for i := range want.Events {
		if !receipted[want.Events[i].EventID] {
			want.Events[i].RequestHashVersion = "unknown"
		}
	}
	reconciled := map[string]bool{}
	for _, tr := range got.Transitions {
		if tr.Cause != "" {
			if tr.Cause != domain.CauseUpgradeReconciliation {
				t.Errorf("unexpected Phase 3 transition after upgrade: %+v", tr)
			}
			reconciled[tr.ObligationID] = true
			// Transitions are listed per obligation: the reconciliation
			// follows its own obligation's recorded history.
			at := len(want.Transitions)
			for i, w := range want.Transitions {
				if w.ObligationID == tr.ObligationID {
					at = i + 1
				}
			}
			want.Transitions = slices.Insert(want.Transitions, at, tr)
			want.LastSeq++
		}
	}
	for i, o := range want.Obligations {
		if reconciled[o.ObligationID] {
			o.Status, o.EvidenceIDs, o.Revision = domain.ObligationUnresolved, nil, o.Revision+1
			want.Obligations[i] = o
		}
	}
	return want
}

func dumpPhase2(t *testing.T, tx store.ReadTx, occurrences []string, viewer domain.Principal) sqlitetest.Phase2Session {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var st sqlitetest.Phase2Session
	var err error
	st.LastSeq = tx.LastSeq()
	st.Items, err = tx.Items(store.ItemFilter{})
	must(err)
	st.Relationships, err = tx.Relationships(store.RelationshipFilter{})
	must(err)
	st.Obligations, err = tx.Obligations("")
	must(err)
	for _, o := range st.Obligations {
		trs, err := tx.ObligationTransitions(o.ObligationID)
		must(err)
		st.Transitions = append(st.Transitions, trs...)
	}
	if ev, err := tx.Event("e-bare"); err == nil {
		st.Events = append(st.Events, ev)
	}
	st.Grants, err = tx.Grants()
	must(err)
	st.Lifecycle, err = tx.LifecycleEvents(store.LifecycleFilter{})
	must(err)
	for _, occ := range occurrences {
		r, err := tx.Receipt(occ)
		must(err)
		st.Receipts = append(st.Receipts, r)
		env, err := tx.Envelope(occ)
		must(err)
		st.Envelopes = append(st.Envelopes, env)
		if r.EventID != "" {
			if ev, err := tx.Event(r.EventID); err == nil {
				st.Events = append(st.Events, ev)
			} else if !errors.Is(err, domain.ErrNotFound) {
				t.Fatal(err)
			}
		}
		if task, err := tx.Task(r.Principal.TaskID); err == nil {
			seen := false
			for _, x := range st.Tasks {
				seen = seen || x.TaskID == task.TaskID
			}
			if !seen {
				st.Tasks = append(st.Tasks, task)
			}
		}
	}
	st.Diagnostics, err = tx.Diagnostics(store.DiagnosticFilter{Viewer: viewer})
	must(err)
	st.Commands, err = tx.LifecycleCommands(store.CommandFilter{Viewer: viewer})
	must(err)
	st.Calls, err = tx.Calls(store.CallFilter{})
	must(err)
	for _, c := range st.Calls {
		as, err := tx.CallAttempts(c.CallID)
		must(err)
		st.Attempts = append(st.Attempts, as...)
		conv, err := tx.Conversation(c.ConversationID)
		must(err)
		st.Conversations = append(st.Conversations, conv)
	}
	return st
}

// assertJSONEqual compares two values by their JSON encoding.
func assertJSONEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.MarshalIndent(want, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != string(w) {
		if dir := os.Getenv("PHASE2_DIFF_DIR"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "got.json"), g, 0o644)
			_ = os.WriteFile(filepath.Join(dir, "want.json"), w, 0o644)
		}
		t.Errorf("%s differs from the Phase 2 record (set PHASE2_DIFF_DIR to write both)", what)
	}
}

// checkReplay verifies each stored request and receipt under its recorded
// (legacy) schema: envelopes re-hash under ingest-payload/v2 and nothing is
// relabeled as Phase 3.
func checkReplay(t *testing.T, st sqlitetest.Phase2Session) {
	t.Helper()
	for _, env := range st.Envelopes {
		if env.SchemaVersion != domain.EventEnvelopeSchemaVersion || env.RequestHashVersion != "" || env.SemanticPolicy != nil || env.Event.Operations != nil {
			t.Errorf("envelope %s relabeled: %+v", env.OccurrenceID, env)
		}
		if err := env.Validate(); err != nil {
			t.Errorf("envelope %s no longer verifies: %v", env.OccurrenceID, err)
		}
	}
	for _, r := range st.Receipts {
		if r.SchemaVersion != domain.IngestReceiptSchemaVersion || r.RequestHashVersion != "" || r.MutationReceiptIDs != nil || r.Operations != nil {
			t.Errorf("receipt %s relabeled: %+v", r.OccurrenceID, r)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("receipt %s no longer validates: %v", r.OccurrenceID, err)
		}
	}
	for _, c := range st.Commands {
		if c.SchemaVersion != domain.LifecycleCommandSchemaVersion || c.Status != domain.CommandParsedNotExecuted || c.Execution != nil {
			t.Errorf("Phase 2 command %s gained execution state: %+v", c.ID, c)
		}
	}
	for _, o := range st.Obligations {
		if o.DeclarationKind != "" || o.BindingState != "" || o.TargetSpec != nil || o.CurrentProofID != "" {
			t.Errorf("legacy obligation %s gained a binding: %+v", o.ObligationID, o)
		}
	}
	for _, it := range st.Items {
		if it.Namespace != "" {
			t.Errorf("legacy item %s gained namespace %q", it.ID, it.Namespace)
		}
	}
	for _, g := range st.Grants {
		if g.Targets != nil {
			t.Errorf("legacy grant %s gained typed targets", g.ID)
		}
	}
}

// checkNothingInvented checks that no Phase 3 record appears for Phase 2
// data: no creation or obligation declarations, leases, membership,
// checkpoints, owners, or GC requests, and a stable-ID obligation grant
// never names an exact version.
func checkNothingInvented(t *testing.T, tx store.ReadTx, st sqlitetest.Phase2Session) {
	t.Helper()
	r, err := store.ReadSemantic(tx)
	if err != nil {
		t.Fatal(err)
	}
	page := store.Page{Limit: 5}
	for _, it := range st.Items {
		checkReconciledDeclaration(t, r, it)
		leases, err := r.LeasesBySource(domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, page)
		if err != nil || len(leases.Records) != 0 {
			t.Errorf("item %s has leases after upgrade: %+v, %v", it.ID, leases.Records, err)
		}
	}
	for _, o := range st.Obligations {
		ref := domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
		if _, err := r.ObligationDeclaration(ref); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("obligation %s has a declaration after upgrade: %v", o.ObligationID, err)
		}
		gs, err := r.GrantsFor(domain.ActionAssertObligation, ref.Target(), 5)
		if err != nil || len(gs) != 0 {
			t.Errorf("a stable-ID grant names obligation %s v%d: %+v, %v", o.ObligationID, o.Version, gs, err)
		}
	}
	for _, c := range st.Conversations {
		if _, err := r.ConversationMembership(c.ConversationID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("conversation %s gained membership: %v", c.ConversationID, err)
		}
	}
	pending, err := r.PendingGCRequests(page)
	if err != nil || len(pending.Records) != 0 {
		t.Errorf("GC requests invented: %+v, %v", pending.Records, err)
	}
	for _, kind := range []domain.OwnerKind{domain.OwnerWorkflow, domain.OwnerAgent} {
		for _, id := range []string{"W", "A"} {
			if _, err := r.OwnerRegistration(kind, id); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("owner %s %s registered by upgrade: %v", kind, id, err)
			}
		}
	}
}

// checkReconciledDeclaration checks migration 0034 (G5, P3-41): an unkeyed
// item gains no creation declaration; a keyed pre-upgrade item gains
// exactly one at its creation sequence under the reconciliation policy,
// either unknown or known with semantics that restate the item's own
// creation fields, never invented ones.
func checkReconciledDeclaration(t *testing.T, r store.SemanticReader, it domain.ContextItem) {
	t.Helper()
	d, err := r.CreationDeclaration(it.ID)
	if it.DirectiveID == "" {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("unkeyed item %s has a creation declaration after upgrade: %v", it.ID, err)
		}
		return
	}
	if err != nil {
		t.Errorf("keyed item %s [%s] has no reconciled declaration: %v", it.ID, it.DirectiveID, err)
		return
	}
	if err := d.Validate(); err != nil || d.ItemID != it.ID || d.Seq != it.Seq || d.PolicyVersion != "legacy-creation-reconciliation/v1" {
		t.Errorf("item %s: reconciled declaration %+v (%v)", it.ID, d, err)
		return
	}
	if !d.LegacyKnown {
		return
	}
	a := d.AcceptedSemantics
	key, _ := it.CurrentKey()
	if a.Key != key || a.Authority != it.Authority || a.Section != it.Section || a.Kind != it.Kind || a.ContentHash != it.ContentHash ||
		a.WorkflowID != it.WorkflowID || a.AgentID != it.AgentID || a.OriginTaskID != it.TaskID || a.OriginTurnID != it.TurnID ||
		a.ObligationDeclarationHash != "" || a.SupportIDs != nil {
		t.Errorf("item %s: known declaration does not restate its creation: %+v", it.ID, a)
	}
}

// checkIndexedReads checks the Phase 3 indexed reads over legacy rows.
func checkIndexedReads(t *testing.T, tx store.ReadTx, sess string, st sqlitetest.Phase2Session) {
	t.Helper()
	if sess != "s1" {
		return
	}
	r, err := store.ReadSemantic(tx)
	if err != nil {
		t.Fatal(err)
	}
	page := store.Page{Limit: 10}
	calls, err := r.ReservingCallsByTask("T", page)
	if err != nil || len(calls.Records) != 1 || calls.Records[0].CallID != "call-1" {
		t.Errorf("ReservingCallsByTask(T) = %+v, %v; want the SENT call", calls.Records, err)
	}
	goals, err := r.OpenGoalsByTaskOwner("T", page)
	if err != nil || len(goals.Records) != 1 || goals.Records[0].DirectiveID != "upgrade" {
		t.Errorf("OpenGoalsByTaskOwner(T) = %+v, %v; want the unresolved goal (Resolve was never executed)", goals.Records, err)
	}
	owned, err := r.ObligationsByTaskOwner("T", page)
	if err != nil || len(owned.Records) != len(st.Obligations) {
		t.Errorf("ObligationsByTaskOwner(T) = %d records, %v; want %d", len(owned.Records), err, len(st.Obligations))
	}
	var goal string
	for _, it := range st.Items {
		if it.Kind == domain.KindGoal {
			goal = it.ID
		}
	}
	gs, err := r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget(sess, goal), 5)
	if err != nil || len(gs) != 1 || gs[0].ID != "g-item" {
		t.Errorf("GrantsFor(resolve, goal) = %+v, %v; want the legacy occurrence grant", gs, err)
	}
}
