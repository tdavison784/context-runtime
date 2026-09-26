package ingest

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-36 state contract: semantic presentation resolves a restatement to the
// current canonical declaration and its lifecycle state; a duplicate's
// receipt snapshot (OPEN/PINNED at creation) never restores a requirement.

// restate ingests setup, applies the lifecycle event, then restates setup
// verbatim, returning the original and the restatement receipts.
func (f *fixture) restate(setup, lifecycle string) (domain.IngestReceipt, domain.IngestReceipt) {
	f.t.Helper()
	sys := principal(domain.AuthoritySystem)
	first := f.mustIngest(sys, sysEvent("orig", setup))
	f.mustIngest(sys, sysEvent("life", lifecycle))
	return first, f.mustIngest(sys, sysEvent("again", setup))
}

// TestP336_ResolvedRestatementStaysResolved (P3-4/36, Q1, C-1): restating
// a resolved goal verbatim records a noncurrent duplicate occurrence; the
// resolved goal stays current and RESOLVED, and nothing reopens.
func TestP336_ResolvedRestatementStaysResolved(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		first, again := f.restate("## Goal [g]\nShip.\n", "## Resolve [g]\n")
		g, dup := mustDirective(t, first, "g"), mustDirective(t, again, "g")
		if len(again.Replacements) != 0 || len(semanticDups(again)) != 1 {
			t.Fatalf("restatement: replacements %+v duplicates %+v", again.Replacements, again.Duplicates)
		}
		if !f.isCurrent(g.ID) || f.isCurrent(dup.ID) {
			t.Fatalf("currentness: original %v, duplicate %v", f.isCurrent(g.ID), f.isCurrent(dup.ID))
		}
		f.view(func(tx store.ReadTx) error {
			it, err := tx.Item(g.ID)
			if err != nil || *it.GoalStatus != domain.GoalResolved {
				t.Errorf("original goal %+v (%v)", it.GoalStatus, err)
			}
			return nil
		})
	})
}

// TestP336_UnpinnedRestatementStaysUnpinned (P3-4/36, Q1): restating an
// unpinned directive verbatim never re-pins it.
func TestP336_UnpinnedRestatementStaysUnpinned(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		first, again := f.restate("## Pinned\n- [p] Keep the API stable.\n", "## Unpin [p]\n")
		p, dup := mustDirective(t, first, "p"), mustDirective(t, again, "p")
		if len(again.Replacements) != 0 || !f.isCurrent(p.ID) || f.isCurrent(dup.ID) {
			t.Fatalf("restatement replaced the unpinned directive: %+v", again.Replacements)
		}
		f.view(func(tx store.ReadTx) error {
			it, err := tx.Item(p.ID)
			if err != nil || it.Generation == domain.GenerationPinned {
				t.Errorf("original pin generation %s (%v)", it.Generation, err)
			}
			return nil
		})
	})
}

// TestP336_ChangedRestatementReplaces (Q1, FR-DIR-002): a restatement with
// changed content is an authorized replacement: a new OPEN current goal
// supersedes the resolved one, which keeps its RESOLVED status.
func TestP336_ChangedRestatementReplaces(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		g := mustDirective(t, f.mustIngest(sys, sysEvent("orig", "## Goal [g]\nShip v1.\n")), "g")
		f.mustIngest(sys, sysEvent("life", "## Resolve [g]\n"))
		r := f.mustIngest(sys, sysEvent("v2", "## Goal [g]\nShip v2.\n"))
		g2 := mustDirective(t, r, "g")
		if len(r.Replacements) != 1 || r.Replacements[0].TargetID != g.ID || !f.isCurrent(g2.ID) || f.isCurrent(g.ID) || *g2.GoalStatus != domain.GoalOpen {
			t.Fatalf("replacement = %+v", r.Replacements)
		}
		f.view(func(tx store.ReadTx) error {
			old, err := tx.Item(g.ID)
			if err != nil || *old.GoalStatus != domain.GoalResolved {
				t.Errorf("replaced goal status %v (%v)", old.GoalStatus, err)
			}
			return nil
		})
	})
}

// TestP336_ReplacementHistoryReconstructible (P3-36): after a chain of
// replacements by different authorities and a later lifecycle change, every
// version keeps its original authority, content, generation at creation and
// transcript-range mapping, and the chain orders them newest to oldest, so
// later delta/rebase logic can identify obsolete raw representations.
func TestP336_ReplacementHistoryReconstructible(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
		// P2 arrives in a two-span event whose operations run the second
		// span first, so the mapping must follow the item's own span.
		r1 := f.mustIngest(user, userEvent("h1", "## Pinned\n- [dep] Use v1.\n", true))
		e2 := domain.Event{EventID: "h2", Kind: domain.EventSystem, Spans: []domain.Span{
			textSpan(domain.AuthoritySystem, false, "Unrelated note."), textSpan(domain.AuthoritySystem, false, "## Pinned\n- [dep] Use v2.\n")},
			Operations: []domain.SemanticOperation{spanOp(1), spanOp(0)}}
		r2 := f.mustIngest(sys, e2)
		f.mustIngest(sys, sysEvent("h3", "## Unpin [dep]\n"))
		p1, p2 := mustDirective(t, r1, "dep"), mustDirective(t, r2, "dep")
		f.view(func(tx store.ReadTx) error {
			chain, err := graph.SupersessionChain(tx, p1.ID)
			if err != nil || len(chain) != 2 || chain[0] != p2.ID || chain[1] != p1.ID {
				t.Fatalf("chain = %v (%v)", chain, err)
			}
			old, err := tx.Item(p1.ID)
			if err != nil || old.Authority != domain.AuthorityUser || old.Parts[0].Text != "Use v1." || old.Generation != domain.GenerationPinned {
				t.Errorf("replaced version changed: %+v (%v)", old, err)
			}
			cur, err := tx.Item(p2.ID)
			if err != nil || cur.Authority != domain.AuthoritySystem || cur.Generation == domain.GenerationPinned {
				t.Errorf("current version: %+v (%v)", cur, err)
			}
			for _, it := range []domain.ContextItem{old, cur} {
				tr, err := tx.Item(it.SourceRanges[0].TranscriptID)
				rg := it.SourceRanges[0].Range
				if err != nil || tr.Role != domain.RoleTranscript || tr.Authority != it.Authority || !strings.Contains(tr.Parts[it.SourceRanges[0].PartIndex].Text[rg.Start:rg.End], it.Parts[0].Text) {
					t.Errorf("%s maps to %+v (%v)", it.ID, tr, err)
				}
			}
			return nil
		})
	})
}
