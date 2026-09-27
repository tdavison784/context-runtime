package storetest

import (
	"sort"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// depSpec is one dependency of an asserted proof: its kind and, for a path
// dependency, its resource-relative path.
type depSpec struct {
	kind domain.ProofDependencyKind
	path string
}

// assertedProof satisfies a new bound obligation id with a
// RESOURCE_BOUND assertion whose proof carries deps on resource "repo".
func assertedProof(t *testing.T, s store.Store, id string, deps ...depSpec) domain.ObligationVersion {
	t.Helper()
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		o = BoundObligation(t, sessA, id, 1, tx.NextSeq(), "src")
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
				Kind: d.kind, ResourceRevision: 1, Fingerprint: fpA, Access: o.Access}
			if d.path != "" {
				dep.Locator = &domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: d.path}
			}
			records = append(records, dep)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		var ids []string
		for _, d := range records {
			ids = append(ids, d.ID)
		}
		user := NewPrincipal(sessA, domain.AuthorityUser)
		p := domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: fpA, ResourceRevision: 1, SemanticMeta: Meta(sessA, proofID, seq),
			Target: Ref(o), TargetSpecHash: spec, TransitionID: trID, RuleVersion: "rule/1", AssertionID: "asr-" + id, DependencyIDs: ids, Access: o.Access}
		noErr(t, sem.InsertApplicabilityProof(p, records))
		noErr(t, sem.InsertAssertion(domain.AssertionRecord{SemanticMeta: Meta(sessA, "asr-"+id, seq), Target: Ref(o), Mode: domain.AssertionResourceBound,
			Actor: user, TransitionID: trID, ProofID: proofID, Access: o.Access}))
		tr := domain.ObligationTransition{ID: trID, SessionID: sessA, ObligationID: id, Version: 1, Seq: seq, From: domain.ObligationUnresolved,
			To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
			AssertionMode: domain.AssertionResourceBound, ProofID: proofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-"+trID, seq), Target: Ref(o), TransitionID: trID, Cause: domain.CauseAssertion,
			ProofID: proofID, AssertionID: "asr-" + id, RuleVersion: "rule/1"}
		o, err = sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	return o
}

// waive moves o's satisfied version to WAIVED, retiring its proof from the
// live indexes.
func waive(t *testing.T, s store.Store, o domain.ObligationVersion) {
	t.Helper()
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		trID := "waive-" + o.ObligationID
		tr := domain.ObligationTransition{ID: trID, SessionID: sessA, ObligationID: o.ObligationID, Version: 1, Seq: seq, From: domain.ObligationSatisfied,
			To: domain.ObligationWaived, Action: domain.ActionWaiveObligation, Actor: NewPrincipal(sessA, domain.AuthorityUser), Cause: domain.CauseWaive,
			PriorProofID: o.CurrentProofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-"+trID, seq), Target: Ref(o), TransitionID: trID, Cause: domain.CauseWaive,
			PreviousProofID: o.CurrentProofID, RuleVersion: "rule/1"}
		_, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, o.Revision)
		return err
	})
}

// testSemanticLiveProofsByPath checks the path-keyed live proof indexes
// (DUR-3.1): a changed path (file or directory) finds exactly the live
// proofs with a CURRENT_PATH dependency at or below it by one key; the
// workspace bucket holds WORKSPACE dependents; FIXED_CONTENT dependencies
// are in no live index; a write-time counter tracks live dependency rows;
// and a
// proof leaves all of them when its version stops resting on it.
func testSemanticLiveProofsByPath(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		return tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "rules"))
	})
	a := assertedProof(t, s, "o-a", depSpec{domain.DependencyCurrentPath, "src/a.go"})
	assertedProof(t, s, "o-b", depSpec{domain.DependencyCurrentPath, "docs/b.md"})
	assertedProof(t, s, "o-w", depSpec{kind: domain.DependencyWorkspace})
	assertedProof(t, s, "o-f", depSpec{domain.DependencyFixedContent, "src/a.go"})
	assertedProof(t, s, "o-both", depSpec{domain.DependencyCurrentPath, "src/sub/c.go"}, depSpec{kind: domain.DependencyWorkspace})
	targets := func(read func(r store.SemanticReader, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error)) []string {
		var out []string
		view(t, s, sessA, func(tx store.ReadTx) error {
			r := readSemantic(t, tx)
			p := store.Page{Limit: 1}
			for {
				pg, err := read(r, p)
				noErr(t, err)
				for _, pr := range pg.Records {
					out = append(out, pr.Target.ObligationID)
				}
				if !pg.More {
					return nil
				}
				p.After = pg.Next
			}
		})
		return out
	}
	byPath := func(path string) []string {
		return targets(func(r store.SemanticReader, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
			return r.LiveProofsByPath("repo", path, p)
		})
	}
	workspace := func() []string {
		return targets(func(r store.SemanticReader, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
			return r.LiveWorkspaceProofs("repo", p)
		})
	}
	dependents := func() uint64 {
		var n uint64
		view(t, s, sessA, func(tx store.ReadTx) error {
			var err error
			n, err = readSemantic(t, tx).LiveProofDependents("repo")
			noErr(t, err)
			return nil
		})
		return n
	}
	for _, c := range []struct {
		path string
		want []string
	}{{"src", []string{"o-a", "o-both"}}, {"src/a.go", []string{"o-a"}}, {"src/sub", []string{"o-both"}}, {"docs", []string{"o-b"}}, {"src/a.go.bak", nil}, {"other", nil}} {
		if got := byPath(c.path); !slicesEqual(got, c.want) {
			t.Errorf("LiveProofsByPath(repo, %s) = %v, want %v", c.path, got, c.want)
		}
	}
	if got := workspace(); !slicesEqual(got, []string{"o-w", "o-both"}) {
		t.Errorf("LiveWorkspaceProofs = %v, want [o-w o-both]", got)
	}
	if n := dependents(); n != 5 {
		t.Errorf("LiveProofDependents = %d, want 5 dependency rows (o-both has two; FIXED_CONTENT o-f has none)", n)
	}
	all := targets(func(r store.SemanticReader, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
		return r.CurrentProofsByDependency("repo", "", p)
	})
	for _, id := range all {
		if id == "o-f" {
			t.Errorf("CurrentProofsByDependency lists the FIXED_CONTENT-only proof: %v", all)
		}
	}
	waive(t, s, a)
	if got := byPath("src"); !slicesEqual(got, []string{"o-both"}) {
		t.Errorf("after waiving o-a: LiveProofsByPath(repo, src) = %v, want [o-both]", got)
	}
	if n := dependents(); n != 4 {
		t.Errorf("after waiving o-a: LiveProofDependents = %d, want 4", n)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		_, err := readSemantic(t, tx).LiveProofsByPath("repo", "../x", store.Page{Limit: 1})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}
