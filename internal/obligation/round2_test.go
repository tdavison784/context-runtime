package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// DUR-2.14 (DUR-1.3 obligation half): a caller may defer the sequence (0);
// the service allocates it only after the replay check, so an exact replay
// consumes no sequence.
func TestDeferredSeqAllocatedAfterReplay_DUR214(t *testing.T) {
	f := newBareFixture(t)
	rep := sessionReporter()
	in := domain.RegisterResourceIntent{RequestID: "reg-deferred", ResourceID: "repo-deferred", Reporter: rep, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}}
	var first, again domain.MutationResult
	var afterFirst, afterReplay uint64
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		first, err = f.s.RegisterResourceTx(tx, rep, in, 0)
		afterFirst = tx.LastSeq()
		return err
	})
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		again, err = f.s.RegisterResourceTx(tx, rep, in, 0)
		afterReplay = tx.LastSeq()
		return err
	})
	if first.Records == nil || again.Records == nil || first.Records.IDs[0] != again.Records.IDs[0] {
		t.Fatalf("replay = %+v, want %+v", again, first)
	}
	if afterReplay != afterFirst {
		t.Errorf("exact replay allocated a sequence: last seq %d -> %d", afterFirst, afterReplay)
	}
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		b, err := r.ResourceBinding("repo-deferred")
		if err != nil || b.Seq != afterFirst {
			t.Errorf("binding = %+v %v, want seq %d", b, err, afterFirst)
		}
		return nil
	})
}

// SPEC-2.3 (P3-39, H4): an observation-state supersession produces one
// SUPERSESSION GC request for the run's task under the recorded policy; the
// first state of a subject supersedes nothing and produces none.
func TestObservationStateSupersessionEnqueuesGC_SPEC23(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)
	r.set(t, f, hashOf("W1"), true)
	requests := func() []domain.GCRequest {
		var out []domain.GCRequest
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			rd, _ := store.ReadSemantic(tx)
			pg, err := rd.PendingGCRequests(store.Page{Limit: 16})
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range pg.Records {
				if g.Trigger == domain.GCSupersession {
					out = append(out, g)
				}
			}
			return nil
		})
		return out
	}
	f.observeTests(t, target, domain.OutcomeFail, hashOf("W1"), nil)
	if got := requests(); len(got) != 0 {
		t.Fatalf("first subject state enqueued %+v", got)
	}
	r.set(t, f, hashOf("W2"), false)
	f.observeTests(t, target, domain.OutcomePass, hashOf("W2"), nil)
	got := requests()
	if len(got) != 1 || got[0].Scope != domain.CollectTask || got[0].TaskID != "task" || got[0].PolicyVersion != testPolicy().Version {
		t.Fatalf("supersession requests = %+v", got)
	}
}
