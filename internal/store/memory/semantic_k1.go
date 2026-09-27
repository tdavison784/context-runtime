package memory

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1 on the memory store: proof validity is derived at read from monotone
// write-time pointers raised in the report's own transaction (K1 A1), the
// settlement-audit scan position (K1 A4), and the live-proof index page
// (K1-api.2). Raise entries are (ResultingAuthoritativeRevision, update
// ID); a resource's revisions are dense and strictly increasing, so the
// ordering is the raises' revision order.

// pathRaise is a pending K1 A1 raise of one changed path's exact key. It
// resolves at commit, when the report's content writes are known: a write
// naming the same update at the same revision that records the path's prior
// content spares the raise (K1-api: same-content path reports do not raise
// it); one that changes it, or no write at all, lets it stand.
type pathRaise struct {
	resource, path, updateID string
	revision                 uint64
}

// pathWrite records one PutResourcePathState of this transaction, by the
// locator's resource-relative path, for pathRaise resolution.
type pathWrite struct {
	resource, path, updateID string
	revision                 uint64
	same                     bool // the write recorded the row's prior content
}

// addPathRaise defers a pending raise and, once per transaction, registers
// its commit-time resolution.
func (t *tx) addPathRaise(r pathRaise) {
	if !t.pathRaiseDone && len(t.pathRaises) == 0 {
		t.deferCheck(func() error {
			t.resolvePathRaises()
			return nil
		})
	}
	t.pathRaises = append(t.pathRaises, r)
}

// recordPathWrite remembers a path content write for raise resolution.
func (t *tx) recordPathWrite(w pathWrite) {
	t.pathWrites = append(t.pathWrites, w)
}

// resolvePathRaises applies the surviving pending raises exactly once. A
// matching write that changed content outranks one that recorded the prior
// content, so the outcome never depends on the writes' order inside the
// transaction; the A5 commit guard also calls this before reading the
// pointers.
func (t *tx) resolvePathRaises() {
	if t.pathRaiseDone {
		return
	}
	t.pathRaiseDone = true
	for _, r := range t.pathRaises {
		same, changed := false, false
		for _, w := range t.pathWrites {
			if w.resource == r.resource && w.path == r.path && w.updateID == r.updateID && w.revision == r.revision {
				if w.same {
					same = true
				} else {
					changed = true
				}
			}
		}
		if changed || !same {
			t.sem.res.affectRaises.add(resPath{r.resource, r.path}, seqRef{r.revision, r.updateID})
		}
	}
}

// raiseAfterID bounds the exclusive cursor of a revision seek: semantic IDs
// never contain 0xff, so (rev, raiseAfterID) precedes every entry of rev.
const raiseAfterID = "\xff"

// affectKey maps a K1 affecting key — "" for ALL, else a canonical
// resource-relative path — to its raise index entry.
func affectKey(resourceID, key string) (resPath, error) {
	if key == "" {
		return resPath{resourceID, allPathsKey}, nil
	}
	if _, err := store.PathAffectKeys(key); err != nil {
		return resPath{}, err
	}
	return resPath{resourceID, key}, nil
}

// raisedUpdate loads the update a raise names, an integrity error if the
// index is ahead of the records.
func (r semRead) raisedUpdate(ref seqRef, what, key string) (domain.ResourceUpdate, error) {
	u, ok := r.r.sem.res.updates.get(ref.id)
	if !ok {
		return domain.ResourceUpdate{}, fmt.Errorf("%w: %s index names missing update %s", domain.ErrIntegrity, what, ref.id)
	}
	return u, nil
}

// LastWorkspaceDivergenceRev implements store.ResourceReader (K1 A1): the
// newest divergence raise's revision, 0 when never raised.
func (r semRead) LastWorkspaceDivergenceRev(resourceID string) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	for ref := range r.r.sem.res.divRaises.before(resourceID, seqRef{}) {
		return ref.seq, nil
	}
	return 0, nil
}

// LastAffectingRev implements store.ResourceReader (K1 A1): the newest
// raise of one exact key — "" for ALL, else a canonical path — 0 when never
// raised. Ancestor keys are the caller's composition, never scanned here.
func (r semRead) LastAffectingRev(resourceID, key string) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	k, err := affectKey(resourceID, key)
	if err != nil {
		return 0, err
	}
	for ref := range r.r.sem.res.affectRaises.before(k, seqRef{}) {
		return ref.seq, nil
	}
	return 0, nil
}

// FirstWorkspaceDivergenceAfter implements store.ResourceReader (K1 A1):
// the earliest divergence raise past rev, a keyset seek over the raises.
func (r semRead) FirstWorkspaceDivergenceAfter(resourceID string, rev uint64) (domain.ResourceUpdate, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceUpdate{}, err
	}
	for ref := range r.r.sem.res.divRaises.after(resourceID, seqRef{rev, raiseAfterID}) {
		return r.raisedUpdate(ref, "workspace divergence", resourceID)
	}
	return domain.ResourceUpdate{}, notFound("workspace divergence update after", resourceID)
}

// FirstAffectingUpdateAfter implements store.ResourceReader (K1 A1): the
// earliest raise of one exact key past rev, a keyset seek over that key
// alone, never a prefix scan.
func (r semRead) FirstAffectingUpdateAfter(resourceID, key string, rev uint64) (domain.ResourceUpdate, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceUpdate{}, err
	}
	k, err := affectKey(resourceID, key)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	for ref := range r.r.sem.res.affectRaises.after(k, seqRef{rev, raiseAfterID}) {
		return r.raisedUpdate(ref, "affecting raise", k.path)
	}
	return domain.ResourceUpdate{}, notFound("affecting update after", k.path)
}

// LiveProofs implements store.ProofReader (K1 A4, K1-api.2): every current
// proof of a current obligation version, in (Seq, ID) keyset order.
func (r semRead) LiveProofs(p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	return page(p, r.r.sem.proof.liveAll.after("", cursorRef(p.After)), loadAll(&r.r.sem.proof.proofs, ident))
}

// The K1 A4 settlement-audit scan position: one unsequenced operational
// record per session, CAS-written on Revision like the GC queue cursor and
// never evidence that a proof was settled.

type settleState struct {
	cursor map[string]store.SettlementCursor // under ""
}

func newSettleState() settleState {
	return settleState{cursor: map[string]store.SettlementCursor{}}
}

type settleView struct {
	cursor table[string, store.SettlementCursor]
}

func newSettleView(st *settleState, w bool) settleView {
	return settleView{cursor: newTable(st.cursor, w, same[store.SettlementCursor])}
}

func (v *settleView) dirty() bool { return v.cursor.dirty() }

func (v *settleView) commit() { v.cursor.commit() }

// SettlementCursor implements store.ReceiptReader (K1 A4).
func (r semRead) SettlementCursor() (store.SettlementCursor, error) {
	if err := r.r.check(); err != nil {
		return store.SettlementCursor{}, err
	}
	c, ok := r.r.sem.settle.cursor.get("")
	if !ok {
		return c, notFound("settlement cursor", r.r.sessionID)
	}
	return c, nil
}

// PutSettlementCursor implements store.ReceiptWriter (K1 A4): a CAS write,
// the session's own cursor only.
func (t *semTx) PutSettlementCursor(c store.SettlementCursor, expectedRevision uint64) (store.SettlementCursor, error) {
	if err := t.t.own(c.Session); err != nil {
		return store.SettlementCursor{}, err
	}
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return store.SettlementCursor{}, err
	}
	cur, _ := t.r.sem.settle.cursor.peek("")
	if cur.Revision != expectedRevision {
		return store.SettlementCursor{}, conflict("settlement cursor: revision %d, expected %d", cur.Revision, expectedRevision)
	}
	t.r.sem.settle.cursor.put("", c)
	return c, nil
}
