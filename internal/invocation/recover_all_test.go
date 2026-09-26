package invocation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestRecoverAllVisitsEverySession (DUR-1.8): startup recovery finds SENT
// calls in every session without the harness tracking session IDs.
func TestRecoverAllVisitsEverySession(t *testing.T) {
	l, s := newMemLedger(t)
	inSession := func(p domain.Principal, id string) domain.Principal { p.SessionID = id; return p }
	sent := map[string]string{}
	for _, id := range []string{"sess-a", "sess-b"} {
		req := request(inSession(agentA, id), "r-"+id, 1, 0, 0)
		req.ServiceActor = inSession(harness, id)
		c, err := l.Prepare(ctx, req)
		must(t, err)
		_, err = l.MarkSent(ctx, req.ServiceActor, c.CallID, "p")
		must(t, err)
		sent[id] = c.CallID
	}
	// A session with semantic state but no calls is visited harmlessly.
	must(t, s.Update(ctx, "sess-c", func(tx store.Tx) error {
		seq := tx.NextSeq()
		return tx.InsertItem(storetest.NewItem("sess-c", "itm-1", seq, "fact"))
	}))

	actorFor := func(id string) domain.Principal { return inSession(harness, id) }
	l = newLedger(s) // restart
	got, err := l.RecoverAll(ctx, actorFor)
	must(t, err)
	if len(got) != 2 || got[0].CallID != sent["sess-a"] || got[1].CallID != sent["sess-b"] {
		t.Fatalf("recovered %+v", got)
	}
	for _, c := range got {
		if c.State != domain.CallUnknown {
			t.Fatalf("call %s is %s", c.CallID, c.State)
		}
	}

	// An actor from the wrong session is reported without stopping the rest.
	c, err := l.Prepare(ctx, func() PrepareRequest {
		r := request(inSession(agentB, "sess-c"), "r-c", 1, 1, 0)
		r.ServiceActor = inSession(harness, "sess-c")
		return r
	}())
	must(t, err)
	_, err = l.MarkSent(ctx, inSession(harness, "sess-c"), c.CallID, "p")
	must(t, err)
	got, err = l.RecoverAll(ctx, func(id string) domain.Principal {
		if id == "sess-a" {
			return inSession(harness, "sess-b")
		}
		return actorFor(id)
	})
	wantErr(t, err, domain.ErrInvalidAuthorityPromotion)
	if len(got) != 2 || got[0].CallID != sent["sess-b"] || got[1].CallID != c.CallID || got[1].State != domain.CallUnknown {
		t.Fatalf("recovered %+v", got)
	}
}

// TestObligationChangeStalesPreview (DUR-1.3): a change to an obligation's
// materialization after a preview must make that preview stale at both
// MarkSent and Prepare. The store's semantic-write rule forces the change to
// carry a fresh sequence number.
func TestObligationChangeStalesPreview(t *testing.T) {
	l, s := newMemLedger(t)
	var obl domain.ObligationVersion
	must(t, s.Update(ctx, sess, func(tx store.Tx) error {
		seq := tx.NextSeq()
		if err := tx.InsertItem(storetest.NewItem(sess, "src", seq, "run the tests")); err != nil {
			return err
		}
		obl = storetest.NewObligation(sess, "obl-1", 1, seq, "src")
		return tx.InsertObligationVersion(obl)
	}))
	preview := lastSeq(t, s)
	c, err := l.Prepare(ctx, request(agentA, "r1", 1, preview, 0))
	must(t, err)

	disable := obl
	disable.MaterializationDisabled = true
	// Without an audit event the store refuses the change outright.
	err = s.Update(ctx, sess, func(tx store.Tx) error {
		_, err := tx.UpdateObligationVersion(disable, obl.Revision)
		return err
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	must(t, s.Update(ctx, sess, func(tx store.Tx) error {
		if _, err := tx.UpdateObligationVersion(disable, obl.Revision); err != nil {
			return err
		}
		ev := storetest.NewLifecycleEvent(sess, "lce-obl-1", tx.NextSeq(), domain.TargetObligation, "obl-1")
		ev.Actor = harness
		return tx.AppendLifecycleEvent(ev)
	}))

	_, err = l.MarkSent(ctx, harness, c.CallID, "p")
	wantErr(t, err, domain.ErrVersionConflict)
	wantState(t, s, c.CallID, domain.CallPrepared)
	_, err = l.Cancel(ctx, harness, c.CallID, "stale preview")
	must(t, err)
	_, err = l.Prepare(ctx, request(agentA, "r1b", 1, preview, 0))
	wantErr(t, err, domain.ErrVersionConflict)
	_, err = l.Prepare(ctx, request(agentA, "r1b", 1, lastSeq(t, s), 0))
	must(t, err)
}
