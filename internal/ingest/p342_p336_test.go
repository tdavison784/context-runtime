package ingest

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/tools"
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

// requireObligations fails the test when the backend lacks the
// obligation/proof/workspace facet: P3-36 expects it on every store, so a
// missing facet is a regression, not pending work.
func requireObligations(t *testing.T, f *fixture) {
	t.Helper()
	if !hasObligations(f.s) {
		t.Fatal("backend lacks the obligation/proof/workspace facet records P3-36 expects")
	}
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
		requireObligations(t, f)
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
		requireObligations(t, f)
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

// TestP336_CheckpointNeverRetiresRequirementBySourceCoverage (P3-36): a
// checkpoint whose parts restate a mandatory requirement and whose coverage
// closes over the rounds around its declaration never retires it: the pin
// stays current, its obligation stays live at the same version with no
// retirement record, and an authorized replacement afterwards still applies
// (coverage does not suppress authority-preserving semantic deltas).
func TestP336_CheckpointNeverRetiresRequirementBySourceCoverage(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		requireObligations(t, f)
		c := newT16(t, f)
		sys := principal(domain.AuthoritySystem)
		rp := f.mustIngest(sys, sysEvent("p336-pin", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v2.\n"))
		pin := mustDirective(t, rp, "dep")
		x1, _ := c.round(1)
		c.external(x1, "db: postgres 16 detected")
		x2, _ := c.round(2)
		c.external(x2, "dependency pinned at v2")
		x3, manifest := c.round(3)
		k, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return c.svc.CreateCheckpoint(tx, c.dispatcher, tools.Request[domain.CheckpointIntent]{Invocation: c.invocation(x3),
				Intent: domain.CheckpointIntent{RequestID: "k336", GenerationManifestID: manifest,
					Parts: textParts("The dependency stays v2 and the tests must pass; steps 1 and 2 done.")}}, seq)
		})
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if !f.isCurrent(pin.ID) {
			t.Fatalf("checkpoint coverage retired the pinned requirement")
		}
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			ck, err := sem.Checkpoint(k.CheckpointID)
			if err != nil || ck.CoveredFrontier != 2 || ck.IssuingExchangeID != x3.ExchangeID {
				t.Fatalf("checkpoint = %+v (%v)", ck, err)
			}
			it, err := tx.Item(pin.ID)
			if err != nil || it.Generation != domain.GenerationPinned || it.Parts[0].Text != "Use dependency v2." {
				t.Errorf("requirement changed: %+v (%v)", it, err)
			}
			obs, err := tx.ObligationsBySource(pin.ID, 10)
			if err != nil || len(obs) != 1 || !obs[0].Current || obs[0].Version != 1 || obs[0].Status != domain.ObligationUnresolved || obs[0].RetiredSeq != 0 {
				t.Errorf("requirement obligation after the checkpoint = %+v (%v)", obs, err)
			}
			if evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: obs[0].ObligationID}); err != nil || len(evs) != 0 {
				t.Errorf("checkpoint wrote obligation lifecycle records: %+v (%v)", evs, err)
			}
			return nil
		})
		// Coverage does not suppress later authorized deltas: replacing the
		// pin after the checkpoint still retires the old version and opens
		// the next one.
		rr := f.mustIngest(sys, sysEvent("p336-rep", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v3.\n"))
		next := mustDirective(t, rr, "dep")
		if !f.isCurrent(next.ID) || f.isCurrent(pin.ID) {
			t.Fatalf("replacement after the checkpoint: new current %v, old current %v", f.isCurrent(next.ID), f.isCurrent(pin.ID))
		}
		f.view(func(tx store.ReadTx) error {
			old, _ := tx.ObligationsBySource(pin.ID, 10)
			cur, _ := tx.ObligationsBySource(next.ID, 10)
			if len(old) != 1 || old[0].Current || old[0].RetiredSeq == 0 {
				t.Errorf("old obligation not retired by the replacement: %+v", old)
			}
			if len(cur) != 1 || !cur[0].Current || cur[0].Version != 2 || cur[0].ObligationID != old[0].ObligationID {
				t.Errorf("new obligation = %+v (old %+v)", cur, old)
			}
			return nil
		})
	})
}
