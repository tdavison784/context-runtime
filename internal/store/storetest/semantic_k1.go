package storetest

import (
	"maps"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1 conformance: proof validity is derived at read from monotone
// write-time pointers (K1 A1, K1-api), the settlement-audit scan position
// (K1 A4, K1-api.2), the live-proof index (K1-api.2), and the one shared
// validity rule store.ProofDerivedValid.

// report accepts one KNOWN update of resource "repo" (revision from+1)
// naming paths, writes the state it produces, and records each path's
// content; the hash for path p is contents[p] when present, else the path
// asserts nothing. It returns the stored update.
func report(t *testing.T, s store.Store, id string, from uint64, fp string, paths []string, contents map[string]string) domain.ResourceUpdate {
	t.Helper()
	var u domain.ResourceUpdate
	update(t, s, sessA, func(tx store.Tx) error {
		u = reportInTx(t, tx, id, from, fp, paths, contents)
		return nil
	})
	return u
}

// reportInTx is report's write inside a caller's transaction, for the A5
// guard's same-transaction cases. Like the service, ChangedPaths and the
// recorded contents are independent: every entry of contents is written,
// whether or not its path is also a changed path (K1-api.3's confirmations
// ride on exactly those writes).
func reportInTx(t *testing.T, tx store.Tx, id string, from uint64, fp string, paths []string, contents map[string]string) domain.ResourceUpdate {
	t.Helper()
	sem := semantic(t, tx)
	u := NewResourceUpdate(sessA, id, "repo", tx.NextSeq(), from, fp, paths...)
	noErr(t, sem.InsertResourceUpdate(u))
	_, err := sem.PutResourceState(StateAfter(u, tx.NextSeq()), from)
	noErr(t, err)
	written := make([]string, 0, len(paths)+len(contents))
	written = append(written, paths...)
	for _, p := range slices.Sorted(maps.Keys(contents)) {
		if !slices.Contains(written, p) {
			written = append(written, p)
		}
	}
	for _, p := range written {
		h, ok := contents[p]
		if !ok {
			continue
		}
		loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: p}
		var expected uint64
		if cur, err := sem.ResourcePathState(loc); err == nil {
			expected = cur.Revision
		} else {
			wantErr(t, err, domain.ErrNotFound)
		}
		_, err = sem.PutResourcePathState(domain.ResourcePathState{SemanticMeta: Meta(sessA, "ps-"+p, tx.NextSeq()), Locator: loc,
			ContentHash: domain.HashBytes([]byte(h)), ResourceUpdateID: u.ID, ResourceRevision: u.ResultingAuthoritativeRevision,
			Revision: 1, Freshness: domain.ResourceKnown}, expected)
		noErr(t, err)
	}
	return u
}

// reportUnknown accepts one UNKNOWN-freshness update (a gap or resynchronization
// report): the service sets AllPaths for them.
func reportUnknown(t *testing.T, s store.Store, id string, from uint64) domain.ResourceUpdate {
	t.Helper()
	var u domain.ResourceUpdate
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		u = NewResourceUpdate(sessA, id, "repo", tx.NextSeq(), from, fpA)
		u.Freshness, u.AllPaths, u.ChangedPaths, u.WorkspaceFingerprint = domain.ResourceUnknown, true, nil, ""
		noErr(t, sem.InsertResourceUpdate(u))
		_, err := sem.PutResourceState(StateAfter(u, tx.NextSeq()), from)
		noErr(t, err)
		return nil
	})
	return u
}

// k1Reads reads the K1 pointers of resource "repo" in one view.
func k1Reads(t *testing.T, s store.Store) (div uint64, affect func(key string) uint64) {
	t.Helper()
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		var err error
		div, err = r.LastWorkspaceDivergenceRev("repo")
		noErr(t, err)
		affect = func(key string) uint64 {
			t.Helper()
			var n uint64
			view(t, s, sessA, func(tx store.ReadTx) error {
				var err error
				n, err = readSemantic(t, tx).LastAffectingRev("repo", key)
				noErr(t, err)
				return nil
			})
			return n
		}
		return nil
	})
	return div, affect
}

// testSemanticK1Pointers checks the write-time pointers and keyset seeks
// (K1 A1): the divergence pointer rises on a changed fingerprint, lost
// freshness and the first report; the affecting pointer rises on the ALL
// key for UNKNOWN and ALL-paths reports, and on a changed path's exact key
// — but not when the report records the path's prior content (K1-api);
// both seek forward keyset-style; a never-raised pointer reads 0 without
// error, and a noncanonical key is rejected.
func testSemanticK1Pointers(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq()))
	})
	// u1 establishes the resource: the first fingerprint counts as changed.
	report(t, s, "u1", 0, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"})
	div, affect := k1Reads(t, s)
	if div != 1 {
		t.Errorf("after u1: divergence = %d, want 1", div)
	}
	if got := affect("src/a.go"); got != 1 {
		t.Errorf("after u1: affecting(src/a.go) = %d, want 1", got)
	}
	if got := affect(""); got != 0 {
		t.Errorf("after u1: affecting(ALL) = %d, want 0", got)
	}
	// u2 reports the same fingerprint and the same content: nothing rises.
	report(t, s, "u2", 1, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"})
	div, affect = k1Reads(t, s)
	if div != 1 || affect("src/a.go") != 1 {
		t.Errorf("after the same-content u2: divergence = %d, affecting = %d, want 1, 1", div, affect("src/a.go"))
	}
	// u3 changes the path's content: its exact key rises, not the
	// fingerprint pointer.
	report(t, s, "u3", 2, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v2"})
	if div, _ = k1Reads(t, s); div != 1 {
		t.Errorf("after u3: divergence = %d, want 1", div)
	}
	if got := affect("src/a.go"); got != 3 {
		t.Errorf("after u3: affecting(src/a.go) = %d, want 3", got)
	}
	// u4 changes the fingerprint: only divergence rises.
	report(t, s, "u4", 3, fpB, []string{"docs/b.md"}, map[string]string{"docs/b.md": "v1"})
	div, affect = k1Reads(t, s)
	if div != 4 {
		t.Errorf("after u4: divergence = %d, want 4", div)
	}
	if got := affect("src/a.go"); got != 3 {
		t.Errorf("after u4: affecting(src/a.go) = %d, want 3", got)
	}
	// u5 loses freshness: divergence rises and the ALL key rises.
	reportUnknown(t, s, "u5", 4)
	div, affect = k1Reads(t, s)
	if div != 5 {
		t.Errorf("after u5: divergence = %d, want 5", div)
	}
	if got := affect(""); got != 5 {
		t.Errorf("after u5: affecting(ALL) = %d, want 5", got)
	}
	// u6 resynchronizes with every path: the ALL key rises again, and so
	// does divergence — the UNKNOWN report left no fingerprint, so a KNOWN
	// one is a change.
	report(t, s, "u6", 5, fpA, nil, nil) // no paths means AllPaths
	div, affect = k1Reads(t, s)
	if div != 6 {
		t.Errorf("after u6: divergence = %d, want 6", div)
	}
	if got := affect(""); got != 6 {
		t.Errorf("after u6: affecting(ALL) = %d, want 6", got)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		// Keyset seeks return the earliest raise past a revision.
		u, err := r.FirstWorkspaceDivergenceAfter("repo", 1)
		noErr(t, err)
		if u.ID != "u4" {
			t.Errorf("FirstWorkspaceDivergenceAfter(repo, 1) = %s, want u4", u.ID)
		}
		u, err = r.FirstAffectingUpdateAfter("repo", "src/a.go", 1)
		noErr(t, err)
		if u.ID != "u3" {
			t.Errorf("FirstAffectingUpdateAfter(repo, src/a.go, 1) = %s, want u3", u.ID)
		}
		u, err = r.FirstAffectingUpdateAfter("repo", "", 5)
		noErr(t, err)
		if u.ID != "u6" {
			t.Errorf("FirstAffectingUpdateAfter(repo, ALL, 5) = %s, want u6", u.ID)
		}
		if _, err := r.FirstWorkspaceDivergenceAfter("repo", 6); !errorsIs(err, domain.ErrNotFound) {
			t.Errorf("FirstWorkspaceDivergenceAfter(repo, 6) error = %v, want ErrNotFound", err)
		}
		// A never-raised resource reads 0 without error.
		n, err := r.LastWorkspaceDivergenceRev("other")
		noErr(t, err)
		if n != 0 {
			t.Errorf("LastWorkspaceDivergenceRev(other) = %d, want 0", n)
		}
		n, err = r.LastAffectingRev("other", "")
		noErr(t, err)
		if n != 0 {
			t.Errorf("LastAffectingRev(other, ALL) = %d, want 0", n)
		}
		// A noncanonical key is rejected.
		if _, err := r.LastAffectingRev("repo", "../x"); !errorsIs(err, domain.ErrInvalidRecord) {
			t.Errorf("LastAffectingRev(repo, ../x) error = %v, want ErrInvalidRecord", err)
		}
		if _, err := r.FirstAffectingUpdateAfter("repo", "/abs", 0); !errorsIs(err, domain.ErrInvalidRecord) {
			t.Errorf("FirstAffectingUpdateAfter(repo, /abs) error = %v, want ErrInvalidRecord", err)
		}
		return nil
	})
}

// testSemanticSettlementCursor checks the settlement-audit scan position
// (K1 A4, K1-api.2): a CAS cursor like the GC queue's, with After either
// the zero start or a full (Seq, ID) pair, session-bound.
func testSemanticSettlementCursor(t *testing.T, s store.Store) {
	read := func() (store.SettlementCursor, error) {
		var c store.SettlementCursor
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			c, err = readSemantic(t, tx).SettlementCursor()
			return nil
		})
		return c, err
	}
	put := func(c store.SettlementCursor, expected uint64) (store.SettlementCursor, error) {
		var out store.SettlementCursor
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			var err error
			out, err = semantic(t, tx).PutSettlementCursor(c, expected)
			return err
		})
		return out, err
	}
	if _, err := read(); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("before the first put: error = %v, want ErrNotFound", err)
	}
	got, err := put(store.SettlementCursor{Session: sessA}, 0)
	noErr(t, err)
	if got.Revision != 1 {
		t.Errorf("created cursor revision = %d, want 1", got.Revision)
	}
	advanced := store.SettlementCursor{Session: sessA, After: store.Cursor{Seq: 9, ID: "proof_a"}}
	if _, err := put(advanced, 0); !errorsIs(err, domain.ErrVersionConflict) {
		t.Errorf("stale revision: error = %v, want ErrVersionConflict", err)
	}
	got, err = put(advanced, 1)
	noErr(t, err)
	if stored, err := read(); err != nil || stored != got || stored.Revision != 2 || stored.After != advanced.After {
		t.Errorf("SettlementCursor = %+v (%v), want %+v", stored, err, got)
	}
	if _, err := put(store.SettlementCursor{Session: sessA, After: store.Cursor{Seq: 3}}, 2); !errorsIs(err, domain.ErrInvalidRecord) {
		t.Errorf("half cursor: error = %v, want ErrInvalidRecord", err)
	}
	if _, err := put(store.SettlementCursor{Session: sessB}, 2); err == nil {
		t.Error("another session's cursor was written")
	}
	// Wrapping to the start is a plain zero After.
	if _, err := put(store.SettlementCursor{Session: sessA}, 2); err != nil {
		t.Errorf("wrap to start: %v", err)
	}
}

// k1AssertedProof satisfies a new bound obligation with a RESOURCE_BOUND
// assertion whose proof carries deps on resource "repo" at revision rev —
// assertedProof with the dependency revision exposed.
func k1AssertedProof(t *testing.T, s store.Store, id string, rev uint64, deps ...depSpec) domain.ObligationVersion {
	t.Helper()
	noErr(t, k1Satisfy(t, s, id, rev, deps...))
	var o domain.ObligationVersion
	view(t, s, sessA, func(tx store.ReadTx) error {
		var err error
		o, err = readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: id, Version: 1})
		noErr(t, err)
		return nil
	})
	return o
}

// k1Satisfy performs k1SatisfyInTx's write in its own transaction, reporting
// the commit's error: the A5 guard refuses at commit, not in the method.
func k1Satisfy(t *testing.T, s store.Store, id string, rev uint64, deps ...depSpec) error {
	t.Helper()
	return s.Update(ctx, sessA, func(tx store.Tx) error {
		return k1SatisfyInTx(t, tx, id, rev, deps...)
	})
}

// k1SatisfyInTx is k1AssertedProof's write inside a caller's transaction,
// for the A5 guard's same-transaction cases.
func k1SatisfyInTx(t *testing.T, tx store.Tx, id string, rev uint64, deps ...depSpec) error {
	t.Helper()
	sem := semantic(t, tx)
	o := BoundObligation(t, sessA, id, 1, tx.NextSeq(), "src")
	noErr(t, tx.InsertObligationVersion(o))
	seq := tx.NextSeq()
	trID := "tr-" + id
	proofID, err := domain.ApplicabilityProofID(Ref(o), trID)
	noErr(t, err)
	spec, err := o.TargetSpec.CanonicalHash()
	noErr(t, err)
	var records []domain.ProofDependency
	for i, d := range deps {
		dep := domain.ProofDependency{SemanticMeta: Meta(sessA, "dep-"+id+"-"+string(rune('a'+i)), seq), ProofID: proofID, ResourceID: "repo",
			Kind: d.kind, ResourceRevision: rev, Fingerprint: fpA, Access: o.Access}
		if d.path != "" {
			dep.Locator = &domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: d.path}
		}
		records = append(records, dep)
	}
	user := NewPrincipal(sessA, domain.AuthorityUser)
	p := domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: fpA, ResourceRevision: rev, SemanticMeta: Meta(sessA, proofID, seq),
		Target: Ref(o), TargetSpecHash: spec, TransitionID: trID, RuleVersion: "rule/1", AssertionID: "asr-" + id,
		DependencyIDs: dependencyIDs(records), Access: o.Access}
	noErr(t, sem.InsertApplicabilityProof(p, records))
	noErr(t, sem.InsertAssertion(domain.AssertionRecord{SemanticMeta: Meta(sessA, "asr-"+id, seq), Target: Ref(o), Mode: domain.AssertionResourceBound,
		Actor: user, TransitionID: trID, ProofID: proofID, Access: o.Access}))
	tr := domain.ObligationTransition{ID: trID, SessionID: sessA, ObligationID: id, Version: 1, Seq: seq, From: domain.ObligationUnresolved,
		To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
		AssertionMode: domain.AssertionResourceBound, ProofID: proofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
	det := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-"+trID, seq), Target: Ref(o), TransitionID: trID, Cause: domain.CauseAssertion,
		ProofID: proofID, AssertionID: "asr-" + id, RuleVersion: "rule/1"}
	_, err = sem.AppendSemanticObligationTransition(tr, det, 1)
	return err
}

// dependencyIDs lists the deps' IDs in insertion order.
func dependencyIDs(records []domain.ProofDependency) []string {
	ids := make([]string, len(records))
	for i, d := range records {
		ids[i] = d.ID
	}
	return ids
}

// k1Setup registers the resource and directive target proofs bind to.
func k1Setup(t *testing.T, s store.Store) {
	t.Helper()
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		return tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "rules"))
	})
}

// testSemanticLiveProofs checks the live-proof index page (K1 A4,
// K1-api.2): every current proof of a current obligation version pages in
// (Seq, ID) keyset order — including FIXED_CONTENT-only proofs — and a
// proof leaves the index when its version stops resting on it.
func testSemanticLiveProofs(t *testing.T, s store.Store) {
	k1Setup(t, s)
	a := k1AssertedProof(t, s, "o-a", 1, depSpec{domain.DependencyCurrentPath, "src/a.go"})
	k1AssertedProof(t, s, "o-b", 1, depSpec{domain.DependencyCurrentPath, "docs/b.md"})
	k1AssertedProof(t, s, "o-w", 1, depSpec{kind: domain.DependencyWorkspace})
	k1AssertedProof(t, s, "o-f", 1, depSpec{domain.DependencyFixedContent, "src/a.go"})
	live := func(p store.Page) []string {
		var out []string
		view(t, s, sessA, func(tx store.ReadTx) error {
			pg, err := readSemantic(t, tx).LiveProofs(p)
			noErr(t, err)
			for _, pr := range pg.Records {
				out = append(out, pr.Target.ObligationID)
			}
			return nil
		})
		return out
	}
	// Page through in (Seq, ID) order with limit 2.
	var got []string
	p := store.Page{Limit: 2}
	for {
		var page []string
		var more bool
		var next store.Cursor
		view(t, s, sessA, func(tx store.ReadTx) error {
			pg, err := readSemantic(t, tx).LiveProofs(p)
			noErr(t, err)
			for _, pr := range pg.Records {
				page = append(page, pr.Target.ObligationID)
			}
			more, next = pg.More, pg.Next
			return nil
		})
		got = append(got, page...)
		if !more {
			break
		}
		p.After = next
	}
	if !slicesEqual(got, []string{"o-a", "o-b", "o-w", "o-f"}) {
		t.Errorf("LiveProofs = %v, want [o-a o-b o-w o-f]", got)
	}
	// A waived version's proof leaves the index.
	waive(t, s, a)
	if got := live(store.Page{Limit: 10}); !slicesEqual(got, []string{"o-b", "o-w", "o-f"}) {
		t.Errorf("after the waiver: LiveProofs = %v, want [o-b o-w o-f]", got)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		if _, err := readSemantic(t, tx).LiveProofs(store.Page{}); !errorsIs(err, domain.ErrInvalidRecord) {
			t.Errorf("zero page limit: error = %v, want ErrInvalidRecord", err)
		}
		return nil
	})
}

// testSemanticProofDerivedValid checks the one shared validity rule
// (K1 A1): WORKSPACE deps fall to the divergence pointer, CURRENT_PATH
// deps to the exact, ancestor and ALL affecting pointers, FIXED_CONTENT
// never falls, and a same-content report spares the exact path.
func testSemanticProofDerivedValid(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// u1 establishes the resource at revision 1 (divergence rises to 1).
	report(t, s, "u1", 0, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"})
	// One view resolves the obligation's current proof and derives its
	// validity; the sqlite store serves one view at a time, so a helper
	// that opened a second view here would deadlock. The obligation must
	// have a current proof, or an expect-invalid case could pass vacuously
	// on an empty proof ID.
	valid := func(id string) bool {
		t.Helper()
		var ok bool
		view(t, s, sessA, func(tx store.ReadTx) error {
			o, err := readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: id, Version: 1})
			noErr(t, err)
			if o.CurrentProofID == "" {
				t.Fatalf("obligation %s has no current proof", id)
			}
			ok, err = store.ProofDerivedValid(readSemantic(t, tx), o.CurrentProofID)
			noErr(t, err)
			return nil
		})
		return ok
	}
	// Dependencies at revision 1 are valid while nothing newer raised.
	k1AssertedProof(t, s, "o-ws", 1, depSpec{kind: domain.DependencyWorkspace})
	k1AssertedProof(t, s, "o-pa", 1, depSpec{domain.DependencyCurrentPath, "src/a.go"})
	k1AssertedProof(t, s, "o-fx", 1, depSpec{domain.DependencyFixedContent, "src/a.go"})
	for _, id := range []string{"o-ws", "o-pa", "o-fx"} {
		if !valid(id) {
			t.Errorf("after u1: proof %s derived invalid, want valid", id)
		}
	}
	// A same-content report spares the exact path and the fingerprint.
	report(t, s, "u2", 1, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"})
	for _, id := range []string{"o-ws", "o-pa", "o-fx"} {
		if !valid(id) {
			t.Errorf("after the same-content u2: proof %s derived invalid, want valid", id)
		}
	}
	// A changed path's exact key fells only path dependencies on it.
	report(t, s, "u3", 2, fpA, []string{"docs/b.md"}, map[string]string{"docs/b.md": "v1"})
	if !valid("o-pa") || !valid("o-ws") || !valid("o-fx") {
		t.Error("after u3 (docs/b.md): src/a.go and workspace proofs fell")
	}
	report(t, s, "u4", 3, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v2"})
	if valid("o-pa") {
		t.Error("after u4 (src/a.go changed): o-pa still derived valid")
	}
	if !valid("o-ws") || !valid("o-fx") {
		t.Error("after u4: workspace and fixed proofs fell")
	}
	// An ancestor directory change fells the paths inside it.
	k1AssertedProof(t, s, "o-sub", 4, depSpec{domain.DependencyCurrentPath, "src/sub/c.go"})
	report(t, s, "u5", 4, fpA, []string{"src"}, nil)
	if valid("o-sub") {
		t.Error("after u5 (src changed): o-sub still derived valid")
	}
	// A changed fingerprint felled the workspace dependency; fixed never.
	report(t, s, "u6", 5, fpB, nil, nil)
	if valid("o-ws") {
		t.Error("after u6 (fingerprint changed): o-ws still derived valid")
	}
	if !valid("o-fx") {
		t.Error("after u6: FIXED_CONTENT proof fell")
	}
	// Lost freshness fells every current-path dependency through ALL.
	k1AssertedProof(t, s, "o-keep", 6, depSpec{domain.DependencyCurrentPath, "other/x.go"})
	reportUnknown(t, s, "u7", 6)
	if valid("o-keep") {
		t.Error("after u7 (UNKNOWN): o-keep still derived valid")
	}
	// An unknown proof fails closed with ErrNotFound.
	view(t, s, sessA, func(tx store.ReadTx) error {
		ok, err := store.ProofDerivedValid(readSemantic(t, tx), "prf-missing")
		if ok || !errorsIs(err, domain.ErrNotFound) {
			t.Errorf("missing proof: valid = %v, error = %v, want false, ErrNotFound", ok, err)
		}
		return nil
	})
}

// testSemanticA5CommitGuard checks the A5 commit guard: a SATISFIED write
// resting on a proof the monotone pointers have already felled is refused
// at commit — atomically, and also when the felling report shares its
// transaction and is written after the satisfying write, so the guard must
// resolve the transaction's pending path raises itself (K1 A5).
func testSemanticA5CommitGuard(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// u1 establishes the resource; u2 changes src/a.go's content, raising
	// its exact affecting key to revision 2.
	report(t, s, "u1", 0, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v1"})
	report(t, s, "u2", 1, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v2"})
	// A dependency below the raised revision is refused at commit.
	if err := k1Satisfy(t, s, "o-stale", 1, depSpec{domain.DependencyCurrentPath, "src/a.go"}); !errorsIs(err, domain.ErrInvalidTransition) {
		t.Errorf("stale dependency: error = %v, want ErrInvalidTransition", err)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		if _, err := readSemantic(t, tx).ExactObligation(domain.ObligationRef{SessionID: sessA, ObligationID: "o-stale", Version: 1}); !errorsIs(err, domain.ErrNotFound) {
			t.Errorf("refused write survived: %v", err)
		}
		return nil
	})
	// A dependency at the raised revision commits.
	if err := k1Satisfy(t, s, "o-fresh", 2, depSpec{domain.DependencyCurrentPath, "src/a.go"}); err != nil {
		t.Errorf("dependency at the raised revision: %v", err)
	}
	// One transaction, adversarial order: the satisfying write is appended
	// before the report that fells it, so the guard must resolve the pending
	// path raise itself rather than trust the resolution's registration
	// order.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		if err := k1SatisfyInTx(t, tx, "o-same", 2, depSpec{domain.DependencyCurrentPath, "src/a.go"}); err != nil {
			return err
		}
		reportInTx(t, tx, "u3", 2, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v3"})
		return nil
	})
	if !errorsIs(err, domain.ErrInvalidTransition) {
		t.Errorf("same-transaction felling: error = %v, want ErrInvalidTransition", err)
	}
	// The same order with a spared raise commits: the report records the
	// path's prior content, so nothing rises past the dependency.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		if err := k1SatisfyInTx(t, tx, "o-keep", 2, depSpec{domain.DependencyCurrentPath, "src/a.go"}); err != nil {
			return err
		}
		reportInTx(t, tx, "u4", 2, fpA, []string{"src/a.go"}, map[string]string{"src/a.go": "v2"})
		return nil
	})
	if err != nil {
		t.Errorf("same-transaction spare: %v", err)
	}
}
