package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// P3-41 (ADR 8 line 1470). The cited cases in testSemanticProofReferences
// cover a noncanonical ID, missing evidence, a dependency-list mismatch,
// another target's spec hash, a missing observation and an unregistered
// dependency resource. This file adds the four missing references, each as
// a COMPLETE bundle — proof, dependencies and the satisfying transition all
// committed in one update, so only the tampered reference can refuse:
//
//   - a wrong-session target: the proof names another session's obligation
//     (canonical ID recomputed for it, so the identity check passes);
//   - a wrong obligation version: the proof names version 2 of o1, which is
//     never stored (canonical ID recomputed likewise);
//   - a dangling coverage reference: EvidenceCoverageID names a coverage
//     that no session ever stored;
//   - a foreign coverage reference: EvidenceCoverageID names a coverage
//     that IS stored — under the other session.
//
// Every case is refused with ErrInvalidRecord and leaves nothing behind,
// and an untampered bundle through the same path commits, so the refusals
// are not vacuous.

func TestP3_41_WrongSessionVersionAndCoverageReferencesRefused(t *testing.T) {
	for _, impl := range []struct {
		name string
		new  func(t *testing.T) store.Store
	}{
		{"memory", func(*testing.T) store.Store { return memory.New() }},
		{"sqlite", func(t *testing.T) store.Store { return sqlitetest.Open(t) }},
	} {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new(t)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			testP3_41References(t, s)
		})
	}
}

func testP3_41References(t *testing.T, s store.Store) {
	t.Helper()
	o := proofWorld(t, s)

	// The other session stores its own evidence and coverage "evcov-b" over
	// it, so a reference to "evcov-b" from this session dangles HERE while
	// really existing in the other session.
	update(t, s, sessB, func(tx store.Tx) error {
		ev := ProducedEvidence(sessB, "ev-b", tx.NextSeq(), "exec-ev-b")
		if err := tx.InsertItem(ev); err != nil {
			return err
		}
		cov, members := NewCoverage(t, sessB, "evcov-b", tx.NextSeq(), domain.CoverageEvidenceSupport, ContentRef(ev))
		return semantic(t, tx).InsertCoverage(cov, members)
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).Coverage("evcov-b")
		noErr(t, err)
		if got.SessionID != sessB {
			t.Fatalf("foreign coverage stored under %s, want %s", got.SessionID, sessB)
		}
		return nil
	})

	// reid recomputes the canonical proof identity after the target was
	// tampered with, so the noncanonical-ID refusal never fires first, and
	// repoints the dependencies at the new ID.
	reid := func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
		t.Helper()
		id, err := domain.ApplicabilityProofID(p.Target, p.TransitionID)
		if err != nil {
			t.Fatal(err)
		}
		p.ID = id
		for i := range deps {
			deps[i].ProofID = id
		}
		return deps
	}

	for _, tc := range []struct {
		name string
		trID string
		edit func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency
	}{
		{"wrong-session target", "tr-p41-sess", func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.Target.SessionID = sessB
			return reid(t, p, deps)
		}},
		{"wrong obligation version", "tr-p41-ver", func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.Target.Version = 2
			return reid(t, p, deps)
		}},
		{"dangling coverage", "tr-p41-dangling", func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.EvidenceCoverageID = "evcov-ghost"
			return deps
		}},
		{"foreign coverage", "tr-p41-foreign", func(t *testing.T, p *domain.ApplicabilityProof, deps []domain.ProofDependency) []domain.ProofDependency {
			p.EvidenceCoverageID = "evcov-b"
			return deps
		}},
	} {
		var proofID string
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			sem := semantic(t, tx)
			seq := tx.NextSeq()
			p, deps := MatcherProof(t, o, tc.trID, "obs1", "ev1", "evcov", seq)
			deps = tc.edit(t, &p, deps)
			proofID = p.ID
			if err := sem.InsertApplicabilityProof(p, deps); err != nil {
				return err
			}
			tr, d := MatcherTransition(o, tc.trID, seq, p, "g-m")
			_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
			return err
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
		view(t, s, sessA, func(tx store.ReadTx) error {
			r := readSemantic(t, tx)
			_, err := r.ApplicabilityProof(proofID)
			wantErr(t, err, domain.ErrNotFound)
			v, err := r.ExactObligation(Ref(o))
			noErr(t, err)
			if v.Status != domain.ObligationUnresolved || v.Revision != 1 || v.CurrentProofID != "" {
				t.Errorf("%s: refused bundle changed the version: %+v", tc.name, v)
			}
			return nil
		})
	}

	// Control: the untampered bundle through the same path commits, and the
	// version follows its proof — so the refusals above name their
	// references, not the bundle shape.
	var okID string
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		p, deps := MatcherProof(t, o, "tr-p41-ok", "obs1", "ev1", "evcov", seq)
		okID = p.ID
		if err := sem.InsertApplicabilityProof(p, deps); err != nil {
			return err
		}
		tr, d := MatcherTransition(o, "tr-p41-ok", seq, p, "g-m")
		got, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		if err != nil {
			return err
		}
		if got.Status != domain.ObligationSatisfied || got.CurrentProofID != p.ID {
			t.Fatalf("control bundle: version = %+v", got)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		v, err := readSemantic(t, tx).ExactObligation(Ref(o))
		noErr(t, err)
		if v.Status != domain.ObligationSatisfied || v.CurrentProofID != okID {
			t.Fatalf("control bundle: version = %+v, want SATISFIED on %s", v, okID)
		}
		return nil
	})
}
