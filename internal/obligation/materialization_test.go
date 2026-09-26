package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (s *Service) setMat(t *testing.T, st store.Store, actor domain.Principal, in domain.SetObligationMaterializationIntent) (domain.MutationResult, error) {
	t.Helper()
	var res domain.MutationResult
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = s.SetMaterializationTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

func TestSetMaterialization(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	setupWorkspace(t, s, st, actorOf(domain.AuthorityHarness))
	userRef, err := pinAndDeclare(t, s, st, "pu", "u", domain.AuthorityUser, "All tests must pass.", "")
	if err != nil {
		t.Fatal(err)
	}
	sysRef, err := pinAndDeclare(t, s, st, "ps", "s", domain.AuthoritySystem, "All tests must pass.", "")
	if err != nil {
		t.Fatal(err)
	}
	user := actorOf(domain.AuthorityUser)
	in := domain.SetObligationMaterializationIntent{RequestID: "m1", Target: *userRef, ExpectedRevision: 1, Disabled: true}

	res, err := s.setMat(t, st, user, in)
	if err != nil || res.Records.Kind != "MATERIALIZATION" {
		t.Fatalf("disable = %+v %v", res, err)
	}
	o, _ := loadObligation(t, st, *userRef)
	if !o.MaterializationDisabled || o.Status != domain.ObligationUnresolved || !o.Current || o.Revision != 2 {
		t.Errorf("after exception: %+v", o)
	}
	if again, err := s.setMat(t, st, user, in); err != nil || again.Records.IDs[0] != res.Records.IDs[0] {
		t.Errorf("replay = %+v %v", again, err)
	}
	var events []domain.LifecycleEvent
	_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		var err error
		events, err = tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation, TargetID: userRef.ObligationID})
		return err
	})
	if len(events) != 1 || events[0].ID != res.Records.IDs[0] || events[0].To != "disabled" || events[0].Actor != user {
		t.Errorf("audit = %+v", events)
	}

	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.SetObligationMaterializationIntent
		want  error
	}{
		"same state":        {user, domain.SetObligationMaterializationIntent{RequestID: "m2", Target: *userRef, ExpectedRevision: 2, Disabled: true}, domain.ErrInvalidTransition},
		"stale revision":    {user, domain.SetObligationMaterializationIntent{RequestID: "m3", Target: *userRef, ExpectedRevision: 1}, domain.ErrVersionConflict},
		"agent":             {actorOf(domain.AuthorityAgent), domain.SetObligationMaterializationIntent{RequestID: "m4", Target: *userRef, ExpectedRevision: 2}, domain.ErrInvalidAuthorityPromotion},
		"user over system":  {user, domain.SetObligationMaterializationIntent{RequestID: "m5", Target: *sysRef, ExpectedRevision: 1, Disabled: true}, domain.ErrInvalidAuthorityPromotion},
		"hidden obligation": {domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem}, domain.SetObligationMaterializationIntent{RequestID: "m6", Target: *sysRef, ExpectedRevision: 1, Disabled: true}, domain.ErrNotFound},
		"unknown version":   {user, domain.SetObligationMaterializationIntent{RequestID: "m7", Target: domain.ObligationRef{SessionID: testSession, ObligationID: userRef.ObligationID, Version: 9}, ExpectedRevision: 1}, domain.ErrNotFound},
	} {
		if _, err := s.setMat(t, st, c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// HARNESS outranks the USER source; re-enable.
	if _, err := s.setMat(t, st, actorOf(domain.AuthorityHarness), domain.SetObligationMaterializationIntent{RequestID: "m8", Target: *userRef, ExpectedRevision: 2}); err != nil {
		t.Errorf("re-enable: %v", err)
	}
}
