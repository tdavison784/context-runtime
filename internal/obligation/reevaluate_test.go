package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var reevalN int

func (f *evalFixture) reevaluate(t *testing.T, actor domain.Principal, ref domain.ObligationRef, rev uint64) (domain.MutationResult, error) {
	t.Helper()
	reevalN++
	in := domain.ReevaluateIntent{RequestID: fmt.Sprintf("re-%d", reevalN), Target: ref, ExpectedRevision: rev}
	var res domain.MutationResult
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = f.s.ReevaluateTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

func TestReevaluateAfterGrant(t *testing.T) {
	f := newEvalFixture(t)
	_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	// A reevaluation without a grant records its selection and changes nothing.
	res, err := f.reevaluate(t, f.harness, f.sysTests, 1)
	if err != nil || len(res.Records.IDs) != 2 || res.Records.IDs[1] != obs.ID {
		t.Fatalf("reevaluate without grant = %+v %v", res, err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("satisfied without grant: %+v", o)
	}
	// A new grant alone never satisfies; the explicit reevaluation does.
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatal("grant issuance satisfied by itself")
	}
	if _, err := f.reevaluate(t, f.harness, f.sysTests, 1); err != nil {
		t.Fatal(err)
	}
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationSatisfied {
		t.Fatalf("reevaluation did not satisfy: %+v", o)
	}
	for name, c := range map[string]struct {
		actor domain.Principal
		rev   uint64
		want  error
	}{
		"USER":           {f.userP, o.Revision, domain.ErrInvalidAuthorityPromotion},
		"AGENT":          {actorOf(domain.AuthorityAgent), o.Revision, domain.ErrInvalidAuthorityPromotion},
		"hidden":         {domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem}, o.Revision, domain.ErrNotFound},
		"stale revision": {f.harness, 1, domain.ErrVersionConflict},
	} {
		if _, err := f.reevaluate(t, c.actor, f.sysTests, c.rev); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// Unbound obligations have nothing to execute.
	unbound, err := pinAndDeclare(t, f.s, f.st, "p-unk", "unk", domain.AuthorityUser, "Ship it.", "deploy_ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.reevaluate(t, f.harness, *unbound, 1); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("unbound reevaluation: %v", err)
	}
}

func TestReevaluateStaleEvidenceAndUnblock(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	// BLOCKED evidence waits for an authorized unblock.
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 1, domain.ObligationBlocked)); err != nil {
		t.Fatal(err)
	}
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationBlocked {
		t.Fatalf("blocked obligation changed: %+v", o)
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 2, domain.ObligationUnresolved)); err != nil {
		t.Fatal(err)
	}
	// The workspace changed since the run: the old evidence no longer applies.
	f.r.set(t, f.fixture, hashOf("W2"), false)
	if _, err := f.reevaluate(t, f.harness, f.sysTests, 3); err != nil {
		t.Fatal(err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("stale evidence satisfied on reevaluation: %+v", o)
	}
	f.r.set(t, f.fixture, hashOf("W1"), false) // reverted content: the W1 run applies again
	res, err := f.reevaluate(t, f.harness, f.sysTests, 3)
	if err != nil {
		t.Fatal(err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("unblock then reevaluate = %+v", o)
	}
	// Exact retry replays the recorded selection.
	var again domain.MutationResult
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		again, err = f.s.ReevaluateTx(tx, f.harness, domain.ReevaluateIntent{RequestID: fmt.Sprintf("re-%d", reevalN), Target: f.sysTests, ExpectedRevision: 3}, tx.NextSeq())
		return err
	})
	if again.Records.IDs[len(again.Records.IDs)-1] != res.Records.IDs[len(res.Records.IDs)-1] {
		t.Errorf("replay = %+v, want %+v", again, res)
	}
}
