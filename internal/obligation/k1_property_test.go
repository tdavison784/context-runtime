package obligation

import (
	"fmt"
	"math/rand/v2"
	"path"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// k1Update is the test's own record of one accepted repo1 report.
type k1Update struct {
	rev            uint64
	unknown, all   bool
	paths          []string
	contents       map[string]string // stated content by path
	fpBefore, fp   string
	contentsBefore map[string]string // known content before the update
}

// k1Model mirrors repo1 independently of the store (K1 A5 oracle).
type k1Model struct {
	updates  []k1Update
	known    bool
	fp       string
	contents map[string]string // current known content; absent = unknown
}

func (m *k1Model) apply(u k1Update) {
	u.fpBefore, u.contentsBefore = m.fp, m.contents
	next := map[string]string{}
	switch {
	case u.unknown:
		m.known = false
	case u.all:
		m.known = true
		for p, h := range u.contents {
			next[p] = h
		}
	default:
		for p, h := range m.contents {
			if !touchesAny(u.paths, p) {
				next[p] = h
			}
		}
		for p, h := range u.contents {
			next[p] = h
		}
	}
	if !u.unknown {
		m.fp = u.fp
	}
	m.contents = next
	m.updates = append(m.updates, u)
}

func touchesAny(changed []string, p string) bool {
	for _, q := range changed {
		if under(p, q) {
			return true
		}
	}
	return false
}

// valid applies the frozen A1 rule to one dependency: invalid iff some
// update after its revision affects it (WORKSPACE: fingerprint changed or
// freshness lost; CURRENT_PATH: UNKNOWN, ALL, or the path or an ancestor
// touched without restating the prior content; FIXED_CONTENT never).
func (m *k1Model) valid(d domain.ProofDependency) bool {
	for _, u := range m.updates {
		if u.rev <= d.ResourceRevision {
			continue
		}
		switch d.Kind {
		case domain.DependencyWorkspace:
			if u.unknown || u.fp != u.fpBefore {
				return false
			}
		case domain.DependencyCurrentPath:
			p := path.Join(d.Locator.BaseDir, d.Locator.Path)
			if !u.unknown && !u.all && !touchesAny(u.paths, p) {
				continue
			}
			prior, hadPrior := u.contentsBefore[p]
			stated, ok := u.contents[p]
			if u.unknown || !ok || !hadPrior || stated != prior {
				return false
			}
		}
	}
	return true
}

// K1 A5: over generated report/observation/assertion histories on both
// stores, stored SATISFIED implies effective SATISFIED or pending
// settlement, and effective SATISFIED is always backed by a proof the
// independent model finds valid.
func TestK1PropertyEffectiveSatisfactionIsValid(t *testing.T) {
	var cov k1Coverage
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { k1History(t, seed, &cov) })
	}
	// The histories must exercise the invariant, not pass vacuously.
	if cov.satisfied < 10 || cov.lost < 3 {
		t.Errorf("coverage: %d effectively SATISFIED checks, %d satisfactions lost; the generator is too weak", cov.satisfied, cov.lost)
	}
}

// k1Coverage counts what the generated histories exercised.
type k1Coverage struct{ satisfied, lost int }

func k1History(t *testing.T, seed uint64, cov *k1Coverage) {
	rng := rand.New(rand.NewPCG(seed, 31))
	f := newEvalFixture(t)
	m := &k1Model{known: true, fp: hashOf("W1"), contents: map[string]string{}}
	m.updates = append(m.updates, k1Update{rev: f.r.auth, all: true, fp: hashOf("W1")})
	f.matcherGrant(t, "g-tests", f.sysTests, TestsPassV1, f.system)
	file := f.fileObligation(t, "70")
	f.matcherGrant(t, "g-file", file, FileReadV1, f.userP)
	paths := []string{"docs/a.md", "docs/b.md"}
	send := func(u k1Update, in domain.ReportResourceChangeIntent) {
		t.Helper()
		f.r.n++
		in.RequestID, in.ResourceID = fmt.Sprintf("k1p-%d-%d", seed, f.r.n), "repo1"
		in.ExpectedRevision, in.ExpectedAuthoritativeRevision = f.r.rev, f.r.auth
		if in.ResultingAuthoritativeRevision == 0 {
			in.ResultingAuthoritativeRevision = f.r.auth + 1
		}
		if _, err := f.s.report(t, f.st, f.harness, in); err != nil {
			t.Fatalf("step report %+v: %v", in, err)
		}
		f.r.rev++
		f.r.auth = in.ResultingAuthoritativeRevision
		u.rev = f.r.auth
		m.apply(u)
	}
	was := map[string]bool{} // effectively SATISFIED at the previous step
	for step := range 40 {
		switch op := rng.IntN(9); {
		case op == 0: // same fingerprint, unrelated path
			send(k1Update{paths: []string{"other/z.md"}, fp: m.fp}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: m.fp, ChangedPaths: []string{"other/z.md"}})
		case op == 1: // new fingerprint
			fp := hashOf(fmt.Sprintf("F%d", step))
			send(k1Update{fp: fp}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: fp})
		case op == 2: // a path with new or same content
			p := paths[rng.IntN(2)]
			h := hashOf(fmt.Sprintf("C%d", rng.IntN(3)))
			send(k1Update{paths: []string{p}, contents: map[string]string{p: h}, fp: m.fp}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: m.fp, ChangedPaths: []string{p}, PathContents: []domain.ResourcePathContent{{Path: p, ContentHash: h}}})
		case op == 3: // the containing directory, no content
			send(k1Update{paths: []string{"docs"}, fp: m.fp}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: m.fp, ChangedPaths: []string{"docs"}})
		case op == 4: // resync stating both paths
			fp := hashOf(fmt.Sprintf("R%d", step))
			contents := map[string]string{}
			var pcs []domain.ResourcePathContent
			for _, p := range paths {
				h := hashOf(fmt.Sprintf("C%d", rng.IntN(3)))
				contents[p] = h
				pcs = append(pcs, domain.ResourcePathContent{Path: p, ContentHash: h})
			}
			send(k1Update{all: true, contents: contents, fp: fp}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: fp, Resynchronization: true, PathContents: pcs})
		case op == 5 && m.known: // a skipped revision loses freshness
			send(k1Update{unknown: true}, domain.ReportResourceChangeIntent{WorkspaceFingerprint: m.fp, ResultingAuthoritativeRevision: f.r.auth + 2})
		case op == 6 && m.known: // tests PASS at the current fingerprint
			f.report(t, f.newRun(t), domain.OutcomePass, m.fp, nil)
		case op == 7 && m.known: // a file read of the current content
			if h, ok := m.contents["docs/a.md"]; ok {
				runN++
				run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")))
				if err != nil {
					t.Fatal(err)
				}
				f.report(t, run, domain.OutcomePass, h, nil)
			}
		case op == 8 && m.known: // tests FAIL at the current fingerprint
			f.report(t, f.newRun(t), domain.OutcomeFail, m.fp, nil)
		}
		k1CheckInvariant(t, f, m, step, []domain.ObligationRef{f.sysTests, file}, cov, was)
	}
}

func k1CheckInvariant(t *testing.T, f *evalFixture, m *k1Model, step int, refs []domain.ObligationRef, cov *k1Coverage, was map[string]bool) {
	t.Helper()
	for _, ref := range refs {
		o := f.status(t, ref)
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			eff, pending, err := EffectiveStatus(r, o)
			if err != nil {
				t.Fatalf("step %d %s: %v", step, ref.ObligationID, err)
			}
			if o.Status == domain.ObligationSatisfied && eff != domain.ObligationSatisfied && !pending {
				t.Errorf("step %d %s: stored SATISFIED is effectively %s without pending settlement", step, ref.ObligationID, eff)
			}
			if was[ref.ObligationID] && eff != domain.ObligationSatisfied {
				cov.lost++
			}
			was[ref.ObligationID] = eff == domain.ObligationSatisfied
			if eff != domain.ObligationSatisfied || o.CurrentProofID == "" {
				return nil
			}
			cov.satisfied++
			pg, err := r.ProofDependencies(o.CurrentProofID, store.Page{Limit: 64})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range pg.Records {
				if d.ResourceID == "repo1" && !m.valid(d) {
					t.Errorf("step %d %s: effectively SATISFIED on a dependency the model finds invalid: %s rev %d",
						step, ref.ObligationID, d.Kind, d.ResourceRevision)
				}
			}
			return nil
		})
	}
}
