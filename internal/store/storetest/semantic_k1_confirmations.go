package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1-api.3 conformance (XREV-5.2): a broad raise — the ALL key or an
// ancestor-directory key — does not invalidate a CURRENT_PATH dependency on
// a path the same report explicitly confirms with the path's prior content,
// while every real change and every omitted path still invalidates it.

// testSemanticK1BroadConfirmations checks the same-content exemption under
// broad reports (K1-api.3, XREV-5.2): the three XREV cases (an ALL-paths
// report, a resynchronization-shaped report with a changed fingerprint, and
// an ancestor-directory report with the path listed unchanged), a
// changed-content control, a broad report that omits the path, W1→W2→W1
// monotonicity for the workspace fingerprint, the exact path and the broad
// keys' confirmation records, interleaved confirmed and unconfirmed raises,
// and that a confirmation for one path never suppresses another path's
// raises.
func testSemanticK1BroadConfirmations(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// valid resolves one obligation's current proof through the shared rule.
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
	// u1 establishes docs/a.md at content H1 (revision 1: divergence and the
	// exact key rise; the ALL key does not).
	report(t, s, "u1", 0, fpA, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H1"})
	k1AssertedProof(t, s, "o-a", 1, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	// A path outside the report's content list: broad raises must still
	// reach it (no global suppression).
	k1AssertedProof(t, s, "o-x", 1, depSpec{domain.DependencyCurrentPath, "src/b.go"})
	// XREV case (a): an ALL-paths report that records docs/a.md's prior
	// content confirms it, so the dependency survives the raise.
	report(t, s, "u2", 1, fpA, nil, map[string]string{"docs/a.md": "H1"})
	if !valid("o-a") {
		t.Error("after the confirming ALL report u2: o-a derived invalid, want valid")
	}
	if valid("o-x") {
		t.Error("after u2: o-x (src/b.go, not confirmed) derived valid, want invalid")
	}

	// A broad report that omits the path does not confirm it.
	report(t, s, "u3", 2, fpA, nil, nil)
	if valid("o-a") {
		t.Error("after the omitting ALL report u3: o-a derived valid, want invalid")
	}
	// A dependency asserted after the unconfirmed raise is valid again...
	k1AssertedProof(t, s, "o-b", 3, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	if !valid("o-b") {
		t.Error("after u3: o-b (at revision 3) derived invalid, want valid")
	}
	// ...and a later confirmation must not resurrect the earlier one
	// (W1→W2→W1 monotonicity of the broad keys' records).
	report(t, s, "u4", 3, fpA, nil, map[string]string{"docs/a.md": "H1"})
	if valid("o-a") {
		t.Error("after the confirming u4: o-a derived valid again, want still invalid")
	}
	if !valid("o-b") {
		t.Error("after u4: o-b derived invalid, want valid")
	}
	// XREV case (b): a resynchronization-shaped ALL report with a changed
	// fingerprint still confirms the path's unchanged content: a
	// CURRENT_PATH dependency survives while a WORKSPACE one falls.
	k1AssertedProof(t, s, "o-w", 4, depSpec{kind: domain.DependencyWorkspace})
	k1AssertedProof(t, s, "o-c", 4, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u5", 4, fpB, nil, map[string]string{"docs/a.md": "H1"})
	if !valid("o-c") {
		t.Error("after the resync-shaped u5: o-c derived invalid, want valid")
	}
	if valid("o-w") {
		t.Error("after u5 (fingerprint changed): o-w derived valid, want invalid")
	}
	// XREV case (c): ChangedPaths naming the ancestor directory and the path
	// itself, with the path's content explicitly unchanged: the ancestor
	// raise is confirmed, the exact key is spared.
	k1AssertedProof(t, s, "o-d", 5, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u6", 5, fpB, []string{"docs", "docs/a.md"}, map[string]string{"docs/a.md": "H1"})
	if !valid("o-d") {
		t.Error("after the ancestor report u6: o-d derived invalid, want valid")
	}
	// Changed content still falls the dependency, whatever the report shape.
	k1AssertedProof(t, s, "o-e", 6, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u7", 6, fpB, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H2"})
	if valid("o-e") {
		t.Error("after the changed-content u7: o-e derived valid, want invalid")
	}
	// H1→H2→H1 on the exact key: a content revert is a change, never a
	// resurrection.
	k1AssertedProof(t, s, "o-f", 7, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u8", 7, fpB, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H3"})
	report(t, s, "u9", 8, fpB, []string{"docs/a.md"}, map[string]string{"docs/a.md": "H1"})
	if valid("o-f") {
		t.Error("after the H1→H2→H1 revert: o-f derived valid, want invalid")
	}
	// Interleaved raises: unconfirmed, then confirming, then unconfirmed
	// again, each judged by the dependency's own revision.
	k1AssertedProof(t, s, "o-g", 9, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u10", 9, fpB, nil, nil)
	if valid("o-g") {
		t.Error("after the omitting u10: o-g derived valid, want invalid")
	}
	k1AssertedProof(t, s, "o-h", 10, depSpec{domain.DependencyCurrentPath, "docs/a.md"})
	report(t, s, "u11", 10, fpB, nil, map[string]string{"docs/a.md": "H1"})
	if valid("o-g") {
		t.Error("after the confirming u11: o-g derived valid again, want still invalid")
	}
	if !valid("o-h") {
		t.Error("after the confirming u11: o-h derived invalid, want valid")
	}
	// W1→W2→W1 on the workspace fingerprint: the divergence pointer only
	// rises, so a workspace dependency never resurrects.
	k1AssertedProof(t, s, "o-w2", 11, depSpec{kind: domain.DependencyWorkspace})
	report(t, s, "u12", 11, fpA, nil, nil)
	if valid("o-w2") {
		t.Error("after the fingerprint revert u12: o-w2 derived valid, want invalid")
	}
}

// testSemanticK1SettlementCause checks the gap-seek cause read
// (K1-api.3 SPEC-2): broad raises that confirmed the path are never
// causes. The earliest unconfirmed raise past rev is, both inside closed
// runs (gap rows) and in the open run after the confirmation pointer; a
// raise at exactly rev is not past it.
func testSemanticK1SettlementCause(t *testing.T, s store.Store) {
	k1Setup(t, s)
	// u1 establishes docs/a.md H1, raising ALL to 1 without confirming;
	// u2 records the same content, confirming the path at 2 and closing
	// the unconfirmed run [1, 1].
	report(t, s, "u1", 0, fpA, nil, map[string]string{"docs/a.md": "H1"})
	report(t, s, "u2", 1, fpA, nil, map[string]string{"docs/a.md": "H1"})
	cause := func(rev uint64) (domain.ResourceUpdate, error) {
		var u domain.ResourceUpdate
		var err error
		view(t, s, sessA, func(tx store.ReadTx) error {
			u, err = readSemantic(t, tx).FirstUnconfirmedAffectingUpdateAfter("repo", "docs/a.md", "", rev)
			return nil
		})
		return u, err
	}
	// At rev 0 the closed run names u1; at u1's own revision the raise is
	// not past r, and past the confirmation every raise confirmed the
	// path: no cause.
	u, err := cause(0)
	noErr(t, err)
	if u.ID != "u1" {
		t.Errorf("cause(repo, docs/a.md, ALL, 0) = %s, want u1", u.ID)
	}
	if u, err := cause(1); !errorsIs(err, domain.ErrNotFound) {
		t.Errorf("cause(repo, docs/a.md, ALL, 1) = %s (%v), want ErrNotFound", u.ID, err)
	}
	// u3 records different content: an unconfirmed raise at 3 opens a run,
	// the cause of any dependency below it.
	report(t, s, "u3", 2, fpA, nil, map[string]string{"docs/a.md": "H2"})
	u, err = cause(1)
	noErr(t, err)
	if u.ID != "u3" {
		t.Errorf("after u3: cause(repo, docs/a.md, ALL, 1) = %s, want u3", u.ID)
	}
	// u4 confirms at 4, closing the run [3, 3]: a dependency at revision 2
	// still takes u3 as its cause; at 3 or later, none.
	report(t, s, "u4", 3, fpA, nil, map[string]string{"docs/a.md": "H2"})
	u, err = cause(2)
	noErr(t, err)
	if u.ID != "u3" {
		t.Errorf("after u4: cause(repo, docs/a.md, ALL, 2) = %s, want u3", u.ID)
	}
	for _, rev := range []uint64{3, 4} {
		if u, err := cause(rev); !errorsIs(err, domain.ErrNotFound) {
			t.Errorf("after u4: cause(repo, docs/a.md, ALL, %d) = %s (%v), want ErrNotFound", rev, u.ID, err)
		}
	}
	// A key that never raised has no cause; the arguments stay canonical.
	view(t, s, sessA, func(tx store.ReadTx) error {
		if _, err := readSemantic(t, tx).FirstUnconfirmedAffectingUpdateAfter("repo", "docs/a.md", "src", 0); !errorsIs(err, domain.ErrNotFound) {
			t.Errorf("never-raised key: error = %v, want ErrNotFound", err)
		}
		if _, err := readSemantic(t, tx).FirstUnconfirmedAffectingUpdateAfter("repo", "../x", "", 0); !errorsIs(err, domain.ErrInvalidRecord) {
			t.Errorf("noncanonical path: error = %v, want ErrInvalidRecord", err)
		}
		return nil
	})
}
