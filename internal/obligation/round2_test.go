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
