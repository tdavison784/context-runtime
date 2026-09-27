package ingest

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// p336Mutate writes the clause's history through ingest: a capable USER pin
// carrying an obligation, replaced twice by SYSTEM (each replacement
// retiring the replaced source's obligation and opening the next version of
// the same obligation identity), then an Unpin of the current version —
// further mutations on top of the replacements and invalidations.
func p336Mutate(t *testing.T, f *fixture) (p1, p2, p3 domain.ContextItem) {
	t.Helper()
	user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
	r1 := f.mustIngest(user, userEvent("m1", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v1.\n", true))
	p1 = mustDirective(t, r1, "dep")
	r2 := f.mustIngest(sys, sysEvent("m2", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v2.\n"))
	p2 = mustDirective(t, r2, "dep")
	r3 := f.mustIngest(sys, sysEvent("m3", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v3.\n"))
	p3 = mustDirective(t, r3, "dep")
	f.mustIngest(sys, sysEvent("m4", "## Unpin [dep]\n"))
	return p1, p2, p3
}

// p336Verify re-reads the whole history: the supersession chain orders the
// versions newest to oldest, every version keeps its original authority,
// content, generation at creation and transcript-range mapping, the
// obligation identity spans the three sources with retired old versions and
// one live new version, and each retirement keeps its causal audit record.
func p336Verify(t *testing.T, f *fixture, p1, p2, p3 domain.ContextItem) {
	t.Helper()
	f.view(func(tx store.ReadTx) error {
		chain, err := graph.SupersessionChain(tx, p1.ID)
		if err != nil || len(chain) != 3 || chain[0] != p3.ID || chain[1] != p2.ID || chain[2] != p1.ID {
			t.Fatalf("chain = %v (%v)", chain, err)
		}
		old1, err := tx.Item(p1.ID)
		if err != nil || old1.Authority != domain.AuthorityUser || old1.Parts[0].Text != "Use dependency v1." || old1.Generation != domain.GenerationPinned {
			t.Errorf("replaced v1 changed: %+v (%v)", old1, err)
		}
		old2, err := tx.Item(p2.ID)
		if err != nil || old2.Authority != domain.AuthoritySystem || old2.Parts[0].Text != "Use dependency v2." {
			t.Errorf("replaced v2 changed: %+v (%v)", old2, err)
		}
		cur, err := tx.Item(p3.ID)
		if err != nil || cur.Authority != domain.AuthoritySystem || cur.Parts[0].Text != "Use dependency v3." || cur.Generation == domain.GenerationPinned {
			t.Errorf("current v3 after Unpin: %+v (%v)", cur, err)
		}
		if ok, _ := graph.IsCurrent(tx, p3.ID); !ok {
			t.Errorf("unpinned v3 not current")
		}
		if ok, _ := graph.IsCurrent(tx, p1.ID); ok {
			t.Errorf("replaced v1 still current")
		}
		for _, it := range []domain.ContextItem{old1, old2, cur} {
			tr, err := tx.Item(it.SourceRanges[0].TranscriptID)
			rg := it.SourceRanges[0].Range
			if err != nil || tr.Role != domain.RoleTranscript || tr.Authority != it.Authority ||
				!strings.Contains(tr.Parts[it.SourceRanges[0].PartIndex].Text[rg.Start:rg.End], it.Parts[0].Text) {
				t.Errorf("%s maps to %+v (%v)", it.ID, tr, err)
			}
		}
		old1Obs, _ := tx.ObligationsBySource(p1.ID, 10)
		old2Obs, _ := tx.ObligationsBySource(p2.ID, 10)
		curObs, _ := tx.ObligationsBySource(p3.ID, 10)
		if len(old1Obs) != 1 || old1Obs[0].Current || old1Obs[0].Version != 1 || old1Obs[0].RetiredSeq == 0 || old1Obs[0].Status != domain.ObligationUnresolved {
			t.Errorf("v1 obligation = %+v", old1Obs)
		}
		if len(old2Obs) != 1 || old2Obs[0].Current || old2Obs[0].Version != 2 || old2Obs[0].RetiredSeq == 0 || old2Obs[0].Status != domain.ObligationUnresolved {
			t.Errorf("v2 obligation = %+v", old2Obs)
		}
		if len(curObs) != 1 || !curObs[0].Current || curObs[0].Version != 3 || curObs[0].Status != domain.ObligationUnresolved {
			t.Errorf("v3 obligation = %+v", curObs)
		}
		oid := old1Obs[0].ObligationID
		if oid == "" || old2Obs[0].ObligationID != oid || curObs[0].ObligationID != oid {
			t.Errorf("obligation identity split: %s / %s / %s", oid, old2Obs[0].ObligationID, curObs[0].ObligationID)
		}
		// The causal records: one retirement audit per replacement, naming
		// the replaced and superseding items, by the replacing authority.
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: oid})
		if err != nil || len(evs) != 2 {
			t.Fatalf("retirement audits = %d (%v)", len(evs), err)
		}
		for i, ev := range evs {
			from, to := p1.ID, p2.ID
			if i == 1 {
				from, to = p2.ID, p3.ID
			}
			if ev.From != from || ev.To != to || ev.Actor.Authority != domain.AuthoritySystem || ev.EventID == "" || ev.Reason != "source directive superseded" {
				t.Errorf("retirement audit %d = %+v", i, ev)
			}
		}
		return nil
	})
}

// TestP336_HistoryReconstructibleAfterFurtherMutationsAndRestart (P3-36):
// replacements by old (USER) and new (SYSTEM) authority, the obligation
// invalidations they cause, and a later lifecycle change all remain
// reconstructible — chain order, per-version authority/content/generation,
// transcript-range mappings, old/new obligation versions and the retirement
// audits — after further mutations, and after closing and reopening the
// store.
func TestP336_HistoryReconstructibleAfterFurtherMutationsAndRestart(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		p1, p2, p3 := p336Mutate(t, f)
		p336Verify(t, f, p1, p2, p3)
	})
	t.Run("sqlite restart", func(t *testing.T) {
		if testing.Short() {
			t.Skip("SQLite backend skipped in -short mode")
		}
		path := sqlitetest.Path(t)
		s, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		f := newFixture(t, s)
		f.usePolicy(testPolicy())
		needsObligations(t, f)
		p1, p2, p3 := p336Mutate(t, f)
		p336Verify(t, f, p1, p2, p3)
		// Restart: reopen the same file and reconstruct the identical
		// history with no further writes.
		before := f.lastSeq()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		g := newFixture(t, reopened)
		g.usePolicy(testPolicy())
		if got := g.lastSeq(); got != before {
			t.Fatalf("reopen changed last seq: %d -> %d", before, got)
		}
		p336Verify(t, g, p1, p2, p3)
	})
}
