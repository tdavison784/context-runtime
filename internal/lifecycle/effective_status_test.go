package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var errDerive = errors.New("unreadable pointer")

// staleProof makes stored-SATISFIED versions resting on proof "stale"
// effectively UNRESOLVED (pending settlement), and "broken" unreadable.
func staleProof(t *testing.T) {
	t.Helper()
	prev := effectiveStatus
	effectiveStatus = func(r store.SemanticReader, o domain.ObligationVersion) (domain.ObligationStatus, bool, error) {
		switch o.CurrentProofID {
		case "valid":
			// A fake proof on a fake reader: answer for the real rule,
			// which reads store pointers since the K1 switch.
			return domain.ObligationSatisfied, false, nil
		case "stale":
			return domain.ObligationUnresolved, true, nil
		case "broken":
			return domain.ObligationUnresolved, false, errDerive
		}
		return prev(r, o)
	}
	t.Cleanup(func() { effectiveStatus = prev })
}

func satisfiedBy(sess, id, source, proof string, seq uint64) domain.ObligationVersion {
	o := storetest.NewObligation(sess, id, 1, seq, source)
	o.Status, o.CurrentProofID = domain.ObligationSatisfied, proof
	return o
}

// assertSatisfied stores obligation "o" on source as SATISFIED through a
// RESOURCE_BOUND assertion and returns its proof ID.
func assertSatisfied(t *testing.T, db store.Store, source string) string {
	t.Helper()
	var proofID string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		if err := sem.InsertResourceBinding(storetest.NewResourceBinding("s", "repo", tx.NextSeq())); err != nil {
			return err
		}
		if err := sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding("s", "wb", "repo", 1, tx.NextSeq())); err != nil {
			return err
		}
		o := storetest.BoundObligation(t, "s", "o", 1, tx.NextSeq(), source)
		if err := tx.InsertObligationVersion(o); err != nil {
			return err
		}
		seq, trID := tx.NextSeq(), "tr-o"
		if proofID, err = domain.ApplicabilityProofID(storetest.Ref(o), trID); err != nil {
			return err
		}
		spec, err := o.TargetSpec.CanonicalHash()
		if err != nil {
			return err
		}
		fp := domain.HashBytes([]byte("workspace A"))
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		dep := domain.ProofDependency{SemanticMeta: storetest.Meta("s", "dep-o", seq), ProofID: proofID, ResourceID: "repo",
			Kind: domain.DependencyWorkspace, ResourceRevision: 1, Fingerprint: fp, Access: o.Access}
		if err := sem.InsertApplicabilityProof(domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: fp, ResourceRevision: 1, SemanticMeta: storetest.Meta("s", proofID, seq),
			Target: storetest.Ref(o), TargetSpecHash: spec, TransitionID: trID, RuleVersion: "rule/1", AssertionID: "asr-o", DependencyIDs: []string{dep.ID}, Access: o.Access},
			[]domain.ProofDependency{dep}); err != nil {
			return err
		}
		if err := sem.InsertAssertion(domain.AssertionRecord{SemanticMeta: storetest.Meta("s", "asr-o", seq), Target: storetest.Ref(o), Mode: domain.AssertionResourceBound,
			Actor: user, TransitionID: trID, ProofID: proofID, Access: o.Access}); err != nil {
			return err
		}
		tr := domain.ObligationTransition{ID: trID, SessionID: "s", ObligationID: "o", Version: 1, Seq: seq, From: domain.ObligationUnresolved,
			To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
			AssertionMode: domain.AssertionResourceBound, ProofID: proofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
		_, err = sem.AppendSemanticObligationTransition(tr, domain.TransitionDetail{SemanticMeta: storetest.Meta("s", "td-"+trID, seq), Target: storetest.Ref(o),
			TransitionID: trID, Cause: domain.CauseAssertion, ProofID: proofID, AssertionID: "asr-o", RuleVersion: "rule/1"}, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return proofID
}

// staleProofID makes the stored-SATISFIED version resting on proof id
// effectively UNRESOLVED ("stale") or unreadable ("broken").
func staleProofID(t *testing.T, id, mode string) {
	t.Helper()
	prev := effectiveStatus
	effectiveStatus = func(r store.SemanticReader, o domain.ObligationVersion) (domain.ObligationStatus, bool, error) {
		if o.CurrentProofID == id && mode != "valid" {
			o.CurrentProofID = mode
		}
		return prev(r, o)
	}
	t.Cleanup(func() { effectiveStatus = prev })
}

// K1 A2: CompleteTask/X8 selects stored-SATISFIED versions too and blocks on
// an effectively UNRESOLVED one; a derivation failure fails closed.
func TestCompletionBlockersUseEffectiveStatus_K1A2(t *testing.T) {
	staleProof(t)
	for proof, want := range map[string]error{"valid": nil, "stale": domain.ErrUnfinishedObligations, "broken": errDerive} {
		r := &completionReads{obligations: func(store.Page) (store.ResultPage[domain.ObligationVersion], error) {
			return store.ResultPage[domain.ObligationVersion]{Records: []domain.ObligationVersion{satisfiedBy("s", "o", "src", proof, 1)}}, nil
		}}
		b := workBudget{remaining: 16, pageSize: 1}
		if err := completionBlockers(r, "task", &b); !errors.Is(err, want) || want == nil && err != nil {
			t.Errorf("%s: %v, want %v", proof, err, want)
		}
	}
}

// K1 A2: GC protection (gc_snapshot OpenObligationSource) keeps the source
// of an effectively UNRESOLVED obligation resident; unreadable never archives.
func TestGCProtectionUsesEffectiveStatus_K1A2(t *testing.T) {
	staleProof(t)
	for _, proof := range []string{"valid", "stale", "broken"} {
		t.Run(proof, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				s, _ := New(db, testPolicy())
				seedEphemeral(t, db, 1, 0)
				staleProofID(t, assertSatisfied(t, db, "eph-000"), proof)
				id := enqueueScratch(t, db, s)
				for range 10 {
					if _, ok := gcResult(t, db, id); ok {
						break
					}
					_, _ = s.CollectPending(context.Background(), "s", func(domain.GCRequest) (domain.Principal, bool) {
						return storetest.NewPrincipal("s", domain.AuthoritySystem), true
					}, 1)
				}
				want := domain.ResidencyResident
				if proof == "valid" {
					want = domain.ResidencyArchived
				}
				if got := residency(t, db, "eph-000"); got != want {
					t.Errorf("source %s, want %s", got, want)
				}
			})
		})
	}
}

// K1 A2: the status-selected selectors behind the lifecycle reads
// (ObligationsByTaskOwner for CompleteTask/X8, ObligationsBySource for GC
// and Archive protection) must return stored-SATISFIED resource-bound
// versions too: neither index may filter on stored status, or the helper
// would never see the versions whose effective status it must decide.
func TestStatusSelectorsReadStoredSatisfiedVersions_K1A2(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		seedEphemeral(t, db, 1, 0)
		assertSatisfied(t, db, "eph-000")
		byOwner, bySource := 0, 0
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			owned, err := sem.ObligationsByTaskOwner("task", store.Page{Limit: 16})
			if err != nil {
				return err
			}
			for _, o := range owned.Records {
				if o.Current && o.Status == domain.ObligationSatisfied && o.BindingState == domain.BindingBound {
					byOwner++
				}
			}
			src, err := tx.ObligationsBySource("eph-000", 16)
			if err != nil {
				return err
			}
			for _, o := range src {
				if o.Current && o.Status == domain.ObligationSatisfied && o.BindingState == domain.BindingBound {
					bySource++
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if byOwner != 1 || bySource != 1 {
			t.Errorf("selectors dropped the stored-SATISFIED bound version: owner=%d source=%d", byOwner, bySource)
		}
	})
}

// K1 A2: explicit Archive discloses removing the source of an effectively
// UNRESOLVED obligation, and fails closed when validity is unreadable.
func TestArchiveProtectionUsesEffectiveStatus_K1A2(t *testing.T) {
	staleProof(t)
	for _, proof := range []string{"valid", "stale", "broken"} {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		s, _ := New(mem, testPolicy())
		seedItem(t, mem, storetest.NewItem("s", "source", 0, "obligation source"))
		staleProofID(t, assertSatisfied(t, mem, "source"), proof)
		r, err := s.ArchiveStandalone(context.Background(), storetest.NewPrincipal("s", domain.AuthorityUser), domain.ArchiveIntent{RequestID: "r", ItemID: "source", ExpectedVersion: 1})
		switch {
		case proof == "broken":
			if !errors.Is(err, errDerive) {
				t.Errorf("broken: %v", err)
			}
		case err != nil || r.ExplicitProtectedRemoval != (proof == "stale"):
			t.Errorf("%s: %+v %v", proof, r, err)
		}
	}
}
