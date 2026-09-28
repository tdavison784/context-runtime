package ingest

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_35_MismatchAndAmbiguityCauseBoundaries closes the P3-42 row
// "mismatch/ambiguity cause boundaries": an AMBIGUOUS or MISMATCH outcome
// narrows its record's DetailAccess to what the outcome depends on — the
// accessible candidates, or the mismatching target — so the source actor
// reads the true outcome and the named target, while another agent of the
// same task reads one uniform WITHHELD record and diagnostic set,
// indistinguishable between the two outcomes and leaking no target ID. The
// boundary is the cause's, inside the transcript boundary; neither outcome
// changes anything it named.
func TestP3_35_MismatchAndAmbiguityCauseBoundaries(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		a := principal(domain.AuthorityUser) // agent A
		b := a
		b.AgentID = "B" // same task, outside A's private boundary

		// AMBIGUOUS: directive key "p" has two current versions the source
		// actor can access — A's private pin and a task-wide pin written by
		// a principal who could not see A's — so A's Unpin names no target.
		f.mustIngest(a, userEvent("p35-a1", "## Pinned\n- [p] {scope=AGENT} private rule\n", true))
		harness := principal(domain.AuthorityHarness)
		harness.AgentID = ""
		f.mustIngest(harness, domain.Event{EventID: "p35-h1", Kind: domain.EventHarness,
			Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, "## Pinned\n- [p] task-wide rule\n")}})
		amb := f.mustIngest(a, userEvent("p35-a2", "## Unpin [p]\n", true))

		// MISMATCH: A's Resolve names their own private pin "q" — a single
		// accessible current target of the wrong kind for Resolve.
		qr := f.mustIngest(a, userEvent("p35-a3", "## Pinned\n- [q] {scope=AGENT} private rule\n", true))
		q := mustDirective(t, qr, "q")
		mis := f.mustIngest(a, userEvent("p35-a4", "## Resolve [q]\n", true))

		if len(amb.Lifecycle) != 1 || len(mis.Lifecycle) != 1 {
			t.Fatalf("commands = %+v / %+v", amb.Lifecycle, mis.Lifecycle)
		}
		ambRec, misRec := amb.Lifecycle[0], mis.Lifecycle[0]
		if ambRec.Resolution != domain.TargetAmbiguous || ambRec.ResolvedItemID != "" || ambRec.ResolvedVersion != 0 ||
			ambRec.Status != domain.CommandNotExecuted || ambRec.Execution == nil || ambRec.Execution.Outcome != domain.CommandOutcomeAmbiguous ||
			len(ambRec.Execution.Diagnostics) == 0 {
			t.Fatalf("ambiguous command = %+v", ambRec)
		}
		if misRec.Resolution != domain.TargetMismatch || misRec.ResolvedItemID != q.ID ||
			misRec.Status != domain.CommandNotExecuted || misRec.Execution == nil || misRec.Execution.Outcome != domain.CommandOutcomeMismatch ||
			len(misRec.Execution.Diagnostics) == 0 {
			t.Fatalf("mismatch command = %+v", misRec)
		}
		if !hasDiag(amb, domain.ErrAmbiguousDirective, domain.ReasonAmbiguousTarget) ||
			!hasDiag(mis, domain.DiagnosticNotFound, domain.ReasonTargetMismatch) {
			t.Fatalf("missing outcome diagnostics: %+v / %+v", amb.Diagnostics, mis.Diagnostics)
		}

		// The boundary each outcome caused: narrowed from the task
		// transcript boundary to the accessible candidates / the target —
		// A's own private boundary — never widened, still readable by the
		// actor, closed to another agent of the same task, while the record
		// itself stays visible at the transcript boundary.
		for name, rec := range map[string]domain.LifecycleCommandRecord{"ambiguous": ambRec, "mismatch": misRec} {
			if rec.SchemaVersion != domain.LifecycleCommandSchemaV2 {
				t.Errorf("%s record is not v2: %+v", name, rec)
			}
			if !rec.DetailAccess.Permits(a) {
				t.Errorf("%s detail boundary hides the source actor: %+v", name, rec.DetailAccess)
			}
			if rec.DetailAccess.Permits(b) {
				t.Errorf("%s detail boundary admits another agent: %+v", name, rec.DetailAccess)
			}
			if rec.DetailAccess.AgentID != a.AgentID || !rec.DetailAccess.Within(rec.Access) {
				t.Errorf("%s detail boundary was not narrowed to its cause: detail %+v, record %+v", name, rec.DetailAccess, rec.Access)
			}
			if !rec.Access.Permits(b) {
				t.Errorf("%s record is invisible at the transcript boundary: %+v", name, rec.Access)
			}
		}

		// The stored reads agree. The actor reads the true outcomes.
		read := func(viewer domain.Principal, occurrence string) domain.LifecycleCommandRecord {
			var out []domain.LifecycleCommandRecord
			f.view(func(tx store.ReadTx) error {
				var err error
				out, err = tx.LifecycleCommands(store.CommandFilter{Viewer: viewer, OccurrenceID: occurrence})
				return err
			})
			if len(out) != 1 {
				t.Fatalf("%s reads %d records for %s", viewer.AgentID, len(out), occurrence)
			}
			return out[0]
		}
		if ambA := read(a, amb.OccurrenceID); ambA.Resolution != domain.TargetAmbiguous || ambA.Execution == nil ||
			ambA.Execution.Outcome != domain.CommandOutcomeAmbiguous {
			t.Errorf("actor's ambiguous record = %+v", ambA)
		}
		if misA := read(a, mis.OccurrenceID); misA.Resolution != domain.TargetMismatch || misA.ResolvedItemID != q.ID ||
			misA.Execution == nil || misA.Execution.Outcome != domain.CommandOutcomeMismatch {
			t.Errorf("actor's mismatch record = %+v", misA)
		}

		// The other agent reads one uniform WITHHELD representation — same
		// status, resolution, empty target, and detail — for both outcomes,
		// with no target ID and no diagnostics.
		ambB, misB := read(b, amb.OccurrenceID), read(b, mis.OccurrenceID)
		for name, rec := range map[string]domain.LifecycleCommandRecord{"ambiguous": ambB, "mismatch": misB} {
			if rec.Status != domain.CommandWithheld || rec.Resolution != domain.TargetWithheld ||
				rec.ResolvedItemID != "" || rec.ResolvedVersion != 0 || rec.Execution == nil ||
				!reflect.DeepEqual(*rec.Execution, domain.CommandExecutionDetail{Outcome: domain.CommandOutcomeWithheld}) {
				t.Errorf("another agent's %s record = %+v", name, rec)
			}
		}
		if ambB.DetailAccess != misB.DetailAccess || ambB.DetailAccess != ambB.Access || misB.DetailAccess != misB.Access {
			t.Errorf("withheld representations differ: %+v vs %+v", ambB, misB)
		}

		// The outcome diagnostics ride the same boundary: the actor's read
		// keeps them, the other agent's read has none of them.
		diags := func(viewer domain.Principal, occurrence string) []domain.DiagnosticRecord {
			var out []domain.DiagnosticRecord
			f.view(func(tx store.ReadTx) error {
				var err error
				out, err = tx.Diagnostics(store.DiagnosticFilter{Viewer: viewer, OccurrenceID: occurrence})
				return err
			})
			return out
		}
		if !hasDiagRecord(diags(a, amb.OccurrenceID), domain.ErrAmbiguousDirective, domain.ReasonAmbiguousTarget) ||
			!hasDiagRecord(diags(a, mis.OccurrenceID), domain.DiagnosticNotFound, domain.ReasonTargetMismatch) {
			t.Error("the actor's diagnostic read lost an outcome diagnostic")
		}
		if hasDiagRecord(diags(b, amb.OccurrenceID), domain.ErrAmbiguousDirective, domain.ReasonAmbiguousTarget) ||
			hasDiagRecord(diags(b, mis.OccurrenceID), domain.DiagnosticNotFound, domain.ReasonTargetMismatch) {
			t.Error("another agent's diagnostic read leaked an outcome diagnostic")
		}

		// Neither outcome changed anything it named: both "p" versions and
		// the mismatching "q" are still current pins.
		if pins := currentIDs(t, f.s, domain.KindConstraint); len(pins) != 3 {
			t.Fatalf("an AMBIGUOUS or MISMATCH outcome changed the pins: %v", pins)
		}
		f.view(func(tx store.ReadTx) error {
			it, err := tx.Item(q.ID)
			if err != nil || it.Generation != domain.GenerationPinned {
				t.Errorf("mismatched target changed: %+v (%v)", it, err)
			}
			return nil
		})
	})
}

// hasDiagRecord is hasDiag over persisted diagnostic records.
func hasDiagRecord(recs []domain.DiagnosticRecord, code domain.DiagnosticCode, reason domain.DiagnosticReason) bool {
	for _, d := range recs {
		if d.Code == code && d.Reason == reason {
			return true
		}
	}
	return false
}
