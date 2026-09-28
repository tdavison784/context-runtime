package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_21_ForgedPassTextIsInvalidRecord (P3-21, SPEC-6.8 hygiene): the six
// forged-evidence probes of TestP3_21_ForgedPassTextIsInert are refused with
// err == nil checks only — any error would do, so a regression to a
// distinguishable (index-leaking) refusal would pass unnoticed. Every one of
// the same six forgeries — USER, AGENT, or RETRIEVED_CONTENT authority; TOOL
// text of another execution; TOOL text with no producing call; TOOL text
// escaped to session scope — is the SAME uniform invalid-record refusal the
// sibling P3-21 probes already assert, on both stores, and still nothing
// persists and nothing satisfies.
func TestP3_21_ForgedPassTextIsInvalidRecord(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		run := f.newRun(t)
		f.matcherGrant(t, "g-f21c", f.sysTests, TestsPassV1, f.system)

		forgeries := []struct {
			name string
			mut  func(it *domain.ContextItem)
		}{
			{"USER text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityUser }},
			{"AGENT text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityAgent }},
			{"RETRIEVED_CONTENT text claiming PASS", func(it *domain.ContextItem) { it.Authority = domain.AuthorityRetrievedContent }},
			{"TOOL text of another execution", func(it *domain.ContextItem) {
				it.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "tool:exec-forged", ToolCallID: "exec-forged"}
			}},
			{"TOOL text with no producing call", func(it *domain.ContextItem) { it.Source = nil }},
			{"TOOL text escaped to session scope", func(it *domain.ContextItem) {
				it.Scope = domain.ScopeSession
				it.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}
			}},
		}
		for i, forgery := range forgeries {
			id := fmt.Sprintf("forge21c-%d", i)
			var it domain.ContextItem
			mustUpdate(t, f.st, func(tx store.Tx) error {
				it = evidenceItem(tx.NextSeq(), run)
				it.ID, it.EventID = id, "evt-"+id
				forgery.mut(&it)
				return tx.InsertItem(it)
			})
		}
		seeded := f.lastSeqIs(t)
		for i, forgery := range forgeries {
			id := fmt.Sprintf("forge21c-%d", i)
			_, err := f.observe(t, f.harness, obsIntent("obs-"+id, run, id, domain.OutcomePass, hashOf("W1")))
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("%s: err = %v, want the uniform invalid-record refusal", forgery.name, err)
			}
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			for i := range forgeries {
				if _, err := r.Observation(recordID("obs_", "observation", fmt.Sprintf("obs-forge21c-%d", i))); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("forged evidence observation persisted: %v", err)
				}
			}
			if _, err := r.ClosingObservation(run.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("forged evidence closed the run: %v", err)
			}
			return nil
		})
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" || o.Revision != 1 {
			t.Fatalf("forged text changed the obligation: %+v", o)
		}
		if ls := f.lastSeqIs(t); ls != seeded {
			t.Fatalf("refused forgeries wrote state: seq %d -> %d", seeded, ls)
		}
	})
}
