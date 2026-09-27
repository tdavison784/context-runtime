package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p3_17TestsPassV2 is a shifted build of the tests matcher: version 2 of the
// same claim, wrapping the compiled tests_pass evaluation.
type p3_17TestsPassV2 struct{ testsPass }

func (p3_17TestsPassV2) Ref() domain.MatcherRef {
	return domain.MatcherRef{Name: "tests_pass", Version: "2"}
}

// TestP3_17_UnavailableMatcherVersionStaysUnresolved closes the P3-42 row
// "unavailable matcher version stays unresolved": a build whose registry
// lacks the exact matcher version an obligation is bound to never substitutes
// any other version — not the newer version of the very same claim. A live
// exact-version grant and complete task-wide PASS observations ingested and
// reevaluated through that build leave the historical binding nonexecutable
// and traceless; a grant for the newer version authorizes nothing either;
// the shifted build satisfies its own /2 binding, proving its registry is
// live; and the very same persisted state satisfies under a build that has
// /1, so only the exact-version rule made the difference.
func TestP3_17_UnavailableMatcherVersionStaysUnresolved(t *testing.T) {
	p3_14BothStores(t, exerciseP3_17UnavailableMatcher)
}

func exerciseP3_17UnavailableMatcher(t *testing.T) {
	t.Helper()
	f := newEvalFixture(t)
	if o := f.status(t, f.sysTests); o.Matcher == nil || *o.Matcher != TestsPassV1 {
		t.Fatalf("obligation is not bound to tests_pass/1: %+v", o)
	}

	// The shifted build: only tests_pass/2 is registered, for the exact ref
	// and for the claim both — "latest" exists under the same name.
	reg := &Registry{byRef: map[domain.MatcherRef]Matcher{}, byClaim: map[string]Matcher{}}
	shifted := p3_17TestsPassV2{}
	reg.byRef[shifted.Ref()] = shifted
	reg.byClaim[shifted.Ref().Name] = shifted
	s2, err := New(testPolicy(), reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.reg.Lookup(TestsPassV1); ok {
		t.Fatal("shifted build still resolves tests_pass/1")
	}

	// Reports and reevaluations ride the shifted build's service, so its
	// registry is the one evaluation consults.
	observe := func() {
		t.Helper()
		runN++
		run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-p317-%d", runN), fmt.Sprintf("exec-p317-%d", runN), f.target))
		if err != nil {
			t.Fatal(err)
		}
		in := obsIntent(fmt.Sprintf("obs-p317-%d", runN), run, evidenceFor(t, f.st, run).ID, domain.OutcomePass, hashOf("W1"))
		mustUpdate(t, f.st, func(tx store.Tx) error {
			sem, err := begin(tx, f.harness, tx.NextSeq())
			if err != nil {
				return err
			}
			_, err = s2.reportObservation(tx, sem, f.harness, in, tx.LastSeq())
			return err
		})
	}
	reevaluate := func(s *Service) {
		t.Helper()
		reevalN++
		in := domain.ReevaluateIntent{RequestID: fmt.Sprintf("re-p317-%d", reevalN), Target: f.sysTests, ExpectedRevision: 1}
		mustUpdate(t, f.st, func(tx store.Tx) error {
			_, err := s.ReevaluateTx(tx, f.system, in, tx.NextSeq())
			return err
		})
	}
	staysUnresolved := func(stage string) {
		t.Helper()
		if st, pending := f.effective(t, f.sysTests); st != domain.ObligationUnresolved || pending {
			t.Fatalf("%s: unavailable matcher version satisfied the obligation: %s pending=%v", stage, st, pending)
		}
		o := f.status(t, f.sysTests)
		if o.CurrentProofID != "" || o.EvidenceIDs != nil || o.Revision != 1 {
			t.Errorf("%s: unavailable matcher left a trace: %+v", stage, o)
		}
		if h := f.history(t, f.sysTests); len(h) != 0 {
			t.Errorf("%s: unavailable matcher produced transitions: %+v", stage, h)
		}
	}

	// A live exact-version grant and a complete task-wide PASS, ingested
	// and reevaluated through the shifted build: nothing happens.
	f.matcherGrant(t, "g-p317", f.sysTests, TestsPassV1, f.system)
	observe()
	reevaluate(s2)
	staysUnresolved("after a /1 grant and a PASS")

	// A grant for the newer version authorizes nothing for the historical
	// binding either: a grant never nominates a matcher version.
	f.matcherGrant(t, "g-p317-v2", f.sysTests, shifted.Ref(), f.system)
	observe()
	reevaluate(s2)
	staysUnresolved("after a /2 grant")

	// The shifted build's own declarations bind /2, and that binding is
	// executable — the registry is live, so the refusal above was the
	// exact-version rule, not a dead registry.
	var v2Ref domain.ObligationRef
	mustUpdate(t, f.st, func(tx store.Tx) error {
		it := seedItemTx(tx, "p-sys-tests-p317", "sys-tests-v2", domain.AuthoritySystem, "All tests must pass.")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		// Its own workspace binding, as the fixture's first source has.
		src := bindIntent("ws-p317", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: "p-sys-tests-p317"})
		if _, err := s2.BindWorkspaceTx(tx, f.system, src, tx.NextSeq()); err != nil {
			return err
		}
		ref, err := s2.DeclarePinnedTx(tx, f.system, it.ID, "", tx.NextSeq())
		if err != nil {
			return err
		}
		v2Ref = *ref
		return nil
	})
	if o := f.status(t, v2Ref); o.Matcher == nil || o.Matcher.Name != "tests_pass" || o.Matcher.Version != "2" {
		t.Fatalf("shifted build did not bind tests_pass/2: %+v", o)
	}
	f.matcherGrant(t, "g-p317-v2own", v2Ref, shifted.Ref(), f.system)
	observe()
	if st, pending := f.effective(t, v2Ref); st != domain.ObligationSatisfied || pending {
		t.Fatalf("shifted build did not satisfy its own /2 binding: %s pending=%v", st, pending)
	}
	staysUnresolved("after the /2 binding satisfied")

	// Control: the very same persisted state — grant, runs, observations —
	// satisfies the historical binding under a build that has /1. Only the
	// registry differed.
	reevaluate(f.s)
	if st, pending := f.effective(t, f.sysTests); st != domain.ObligationSatisfied || pending {
		t.Fatalf("control through the /1 registry did not satisfy: %s pending=%v", st, pending)
	}
}
