package ingest

import (
	"fmt"
	"reflect"
	"testing"

	"context"
	"github.com/tdavison784/context-runtime/internal/domain"

	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestUnknownIdentityRestatementIsALineDiagnostic is SPEC-1.3 (ruling 3,
// FROZEN C-1, P3-4/41): an identical restatement of a pre-upgrade directive
// whose creation identity is unknown (no or an unknown creation
// declaration) can be neither a duplicate nor a replacement. That one line
// becomes an ErrUnsupportedDirective/unknown_identity diagnostic: nothing is
// written for it, the prior stays current with its obligations untouched,
// and the rest of the event, including a new directive in the same span,
// is ingested. A retry replays the receipt.
func TestUnknownIdentityRestatementIsALineDiagnostic(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		// Recreate a migrated pre-upgrade pin whose creation identity could
		// not be reconciled: migration 0034 records an unknown declaration.
		old, err := f.in.Ingest(ctx, unknownDeclStore{f.s}, sys, sysEvent("legacy", "## Pinned\n- [p] Keep tests green.\n"))
		if err != nil {
			t.Fatal(err)
		}
		prior := mustDirective(t, old, "p")
		before := snapshotPhase2(t, f.s)

		e := sysEvent("restate", "Context.\n## Pinned\n- [p] Keep tests green.\n- [q] A new rule.\n")
		r, err := f.ingest(sys, e)
		if err != nil {
			t.Fatalf("restatement aborted the event: %v", err)
		}
		if _, ok := byDirective(r, "p"); ok {
			t.Fatal("an item was written for the unknown-identity restatement")
		}
		if len(r.Duplicates)+len(r.Replacements) != 0 {
			t.Fatalf("restatement linked: dups %+v repls %+v", r.Duplicates, r.Replacements)
		}
		q := mustDirective(t, r, "q")
		if !f.isCurrent(q.ID) || !f.isCurrent(prior.ID) {
			t.Fatalf("current: q=%v prior=%v", f.isCurrent(q.ID), f.isCurrent(prior.ID))
		}
		var found int
		for _, d := range r.Diagnostics {
			if d.Reason == domain.ReasonUnknownIdentity {
				found++
				if d.Code != domain.ErrUnsupportedDirective || d.DirectiveID != "p" {
					t.Fatalf("diagnostic = %+v", d.Diagnostic)
				}
			}
		}
		if found != 1 {
			t.Fatalf("unknown-identity diagnostics = %d, want 1: %+v", found, r.Diagnostics)
		}
		after := snapshotPhase2(t, f.s)
		if !reflect.DeepEqual(after.Versions, before.Versions) || !reflect.DeepEqual(after.Transitions, before.Transitions) {
			t.Fatal("the restatement changed the prior's obligations")
		}
		seq := f.lastSeq()
		again, err := f.ingest(sys, e)
		if err != nil || !reflect.DeepEqual(normReceipt(again), normReceipt(r)) || f.lastSeq() != seq {
			t.Fatalf("retry: %v", err)
		}
		// A changed restatement of the same key is an ordinary authorized
		// replacement; only an identical one has no defined identity.
		c := f.mustIngest(sys, sysEvent("change", "## Pinned\n- [p] Keep all tests green.\n"))
		if len(c.Replacements) != 1 || c.Replacements[0].TargetID != prior.ID {
			t.Fatalf("changed restatement: %+v", c.Replacements)
		}
	})
}

// unknownDeclStore records every creation declaration written through it
// as unknown identity (LegacyKnown=false, no signature or semantics), the
// form migration 0034 gives a pre-upgrade item it cannot reconcile.
type unknownDeclStore struct{ store.Store }

func (s unknownDeclStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, sessionID, func(tx store.Tx) error { return fn(unknownDeclTx{tx}) })
}

type unknownDeclTx struct{ store.Tx }

func (t unknownDeclTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(t.Tx)
	if err != nil {
		return nil, err
	}
	return unknownDeclSem{sem}, nil
}
func (t unknownDeclTx) SemanticReadBackend() store.SemanticReader {
	r, _ := store.ReadSemantic(t.Tx)
	return r
}

type unknownDeclSem struct{ store.SemanticTx }

func (s unknownDeclSem) InsertCreationDeclaration(d domain.CreationDeclaration) error {
	d.Signature, d.LegacyKnown, d.AcceptedSemantics = "", false, domain.CreationSemantics{}
	return s.SemanticTx.InsertCreationDeclaration(d)
}

// TestUnknownIdentityWorkingSnapshotDedups_SPEC29 (SPEC-2.9, G5, C-1;
// commander ruling: attribute-free classes match over unknown identity): an
// identical Working snapshot restated over members whose creation identity
// is unknown (pre-upgrade, unreconciled) is a duplicate. It links
// DUPLICATE_OF, replaces and rebinds nothing, never aborts the event, and
// the rest of the event is ingested. A changed snapshot still replaces.
func TestUnknownIdentityWorkingSnapshotDedups_SPEC29(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("start", "hello"))
		priors := f.legacyUnknownWorking("## Working\n- step one\n- step two\n")
		r, err := f.ingest(sys, sysEvent("restate-w", "## Working\n- step one\n- step two\n## Pinned\n- [q] A new rule.\n"))
		if err != nil {
			t.Fatalf("identical snapshot over unknown identity aborted the event: %v", err)
		}
		if len(r.Replacements) != 0 || len(r.Duplicates) != len(priors) {
			t.Fatalf("restatement: dups %+v repls %+v, want %d duplicates", r.Duplicates, r.Replacements, len(priors))
		}
		for _, id := range priors {
			if !f.isCurrent(id) {
				t.Fatalf("prior member %s is no longer current", id)
			}
		}
		if q := mustDirective(t, r, "q"); !f.isCurrent(q.ID) {
			t.Fatal("the rest of the event was not ingested")
		}
		c := f.mustIngest(sys, sysEvent("change-w", "## Working\n- step three\n"))
		if len(c.Replacements) != len(priors) {
			t.Fatalf("changed snapshot replaced %d of %d", len(c.Replacements), len(priors))
		}
	})
}

// legacyUnknownWorking files the Working snapshot text in task T the way a
// pre-upgrade snapshot that migration 0034 could not reconcile looks: the
// exact rows ingest would write (minted by ingesting it in another task),
// with no namespace, an unknown creation declaration, no snapshot
// declaration, and current. It returns the member IDs.
func (f *fixture) legacyUnknownWorking(text string) []string {
	f.t.Helper()
	probe := principal(domain.AuthoritySystem)
	probe.TaskID = "T-probe"
	e := sysEvent("probe-"+domain.HashBytes([]byte(text))[7:19], text)
	e.Spans[0].Access.TaskID = "T-probe"
	var ids []string
	members := semantic(f.mustIngest(probe, e))
	if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		for i, it := range members {
			it.ID = fmt.Sprintf("itm_legacy_working_%d", i)
			it.TaskID, it.Access.TaskID, it.Namespace, it.SourceRanges = "T", "T", "", nil
			it.Seq = tx.NextSeq()
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
				return err
			}
			if err := sem.InsertCreationDeclaration(domain.CreationDeclaration{SemanticMeta: domain.SemanticMeta{ID: "decl_" + it.ID, SessionID: sess,
				SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, ItemID: it.ID, PolicyVersion: "legacy-creation-reconciliation/v1"}); err != nil {
				return err
			}
			ids = append(ids, it.ID)
		}
		return nil
	}); err != nil {
		f.t.Fatalf("legacy Working snapshot: %v", err)
	}
	return ids
}
