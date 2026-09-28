package memory

import (
	"fmt"
	"path"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Obligation declarations, applicability proofs, assertions, semantic
// transitions and their derived reads (P3-12..18, P3-23). Stores enforce
// exact references, the transition table, CAS, and status/cache atomicity;
// bundled records that name each other (proof <-> transition, assertion <->
// transition) are checked at commit. Services authorize and prove
// applicability.

// depKey indexes the live dependencies of current proofs: path is the
// canonical locator key of a path dependency, or empty for a
// workspace-level dependency.
type depKey struct{ resource, path string }

type proofState struct {
	decls       map[obligationKey]domain.ObligationDeclaration
	declIDs     map[string]bool
	proofs      map[string]domain.ApplicabilityProof
	deps        map[string]domain.ProofDependency
	depsByProof map[string][]seqRef
	assertions  map[string]domain.AssertionRecord
	details     map[string]domain.TransitionDetail // by transition ID
	trByVersion map[obligationKey][]seqRef
	owners      map[string][]seqRef // task -> current TURN/TASK-owned versions
	bound       map[string][]seqRef // subject key -> current bound versions
	liveDeps    map[depKey][]seqRef // current proofs by exact dependency
	liveByRes   map[string][]seqRef // current proofs by resource
	liveByPath  map[depKey][]seqRef // live CURRENT_PATH dependents by (resource, ancestor path) (DUR-3.1)
	liveWS      map[string][]seqRef // live WORKSPACE dependents by resource (DUR-3.1)
	liveAll     map[string][]seqRef // every live proof under "" in (Seq, ID) order (K1 A4)
	dependents  map[string]uint64   // live non-FIXED dependency rows per resource (DUR-3.1)
}

func newProofState() proofState {
	return proofState{
		decls: map[obligationKey]domain.ObligationDeclaration{}, declIDs: map[string]bool{}, proofs: map[string]domain.ApplicabilityProof{},
		deps: map[string]domain.ProofDependency{}, depsByProof: map[string][]seqRef{}, assertions: map[string]domain.AssertionRecord{},
		details: map[string]domain.TransitionDetail{}, trByVersion: map[obligationKey][]seqRef{}, owners: map[string][]seqRef{},
		bound: map[string][]seqRef{}, liveDeps: map[depKey][]seqRef{}, liveByRes: map[string][]seqRef{},
		liveByPath: map[depKey][]seqRef{}, liveWS: map[string][]seqRef{}, liveAll: map[string][]seqRef{}, dependents: map[string]uint64{},
	}
}

type proofView struct {
	decls       table[obligationKey, domain.ObligationDeclaration]
	declIDs     table[string, bool]
	proofs      table[string, domain.ApplicabilityProof]
	deps        table[string, domain.ProofDependency]
	depsByProof orderedIndex[string]
	assertions  table[string, domain.AssertionRecord]
	details     table[string, domain.TransitionDetail]
	trByVersion orderedIndex[obligationKey]
	owners      orderedIndex[string]
	bound       orderedIndex[string]
	liveDeps    orderedIndex[depKey]
	liveByRes   orderedIndex[string]
	liveByPath  orderedIndex[depKey]
	liveWS      orderedIndex[string]
	liveAll     orderedIndex[string]
	dependents  table[string, uint64]
}

func newProofView(st *proofState, w bool) proofView {
	return proofView{
		decls: newTable(st.decls, w, domain.ObligationDeclaration.Clone), declIDs: newTable(st.declIDs, w, same[bool]),
		proofs: newTable(st.proofs, w, domain.ApplicabilityProof.Clone), deps: newTable(st.deps, w, domain.ProofDependency.Clone),
		depsByProof: newOrderedIndex(st.depsByProof, w), assertions: newTable(st.assertions, w, domain.AssertionRecord.Clone),
		details: newTable(st.details, w, domain.TransitionDetail.Clone), trByVersion: newOrderedIndex(st.trByVersion, w),
		owners: newOrderedIndex(st.owners, w), bound: newOrderedIndex(st.bound, w), liveDeps: newOrderedIndex(st.liveDeps, w),
		liveByRes: newOrderedIndex(st.liveByRes, w), liveByPath: newOrderedIndex(st.liveByPath, w),
		liveWS: newOrderedIndex(st.liveWS, w), liveAll: newOrderedIndex(st.liveAll, w), dependents: newTable(st.dependents, w, same[uint64]),
	}
}

func (v *proofView) dirty() bool {
	return v.decls.dirty() || v.proofs.dirty() || v.assertions.dirty() || v.details.dirty()
}

func (v *proofView) commit() {
	v.decls.commit()
	v.declIDs.commit()
	v.proofs.commit()
	v.deps.commit()
	v.depsByProof.commit()
	v.assertions.commit()
	v.details.commit()
	v.trByVersion.commit()
	v.owners.commit()
	v.bound.commit()
	v.liveDeps.commit()
	v.liveByRes.commit()
	v.liveByPath.commit()
	v.liveWS.commit()
	v.liveAll.commit()
	v.dependents.commit()
}

func refKey(r domain.ObligationRef) obligationKey { return obligationKey{r.ObligationID, r.Version} }

// versionRef is an obligation version's index entry: its creation sequence
// and obligation ID, unique among current versions.
func versionRef(o domain.ObligationVersion) seqRef { return seqRef{o.CreatedSeq, o.ObligationID} }

// noteObligation keeps the obligation indexes in step with a version write:
// task ownership and subject binding of current versions, and the live
// dependency index of the current proof.
func (t *tx) noteObligation(before, after domain.ObligationVersion) {
	owned := func(o domain.ObligationVersion) (string, bool) {
		return o.Access.TaskID, o.Current && o.Access.TaskID != "" && (o.Access.Scope == domain.ScopeTask || o.Access.Scope == domain.ScopeTurn)
	}
	boundKey := func(o domain.ObligationVersion) (string, bool) {
		return o.TargetSubjectKey, o.Current && o.BindingState == domain.BindingBound
	}
	for _, ix := range []struct {
		index *orderedIndex[string]
		key   func(domain.ObligationVersion) (string, bool)
	}{{&t.sem.proof.owners, owned}, {&t.sem.proof.bound, boundKey}} {
		bk, bok := ix.key(before)
		ak, aok := ix.key(after)
		if bok && (!aok || bk != ak) {
			ix.index.remove(bk, versionRef(before))
		}
		if aok && (!bok || bk != ak) {
			ix.index.add(ak, versionRef(after))
		}
	}
	oldProof, newProof := "", ""
	if before.Current {
		oldProof = before.CurrentProofID
	}
	if after.Current {
		newProof = after.CurrentProofID
	}
	if oldProof != newProof {
		t.indexProofDeps(oldProof, false)
		t.indexProofDeps(newProof, true)
	}
}

// indexProofDeps adds or removes a proof's dependencies in the live index.
// A proof not stored yet is indexed when it is inserted.
func (t *tx) indexProofDeps(proofID string, live bool) {
	p, ok := t.sem.proof.proofs.peek(proofID)
	if proofID == "" || !ok {
		return
	}
	ref := seqRef{p.Seq, p.ID}
	// The audit worker's index holds every live proof — the current proof
	// of a current obligation version — FIXED_CONTENT-only ones included
	// (K1 A4, K1-api.2).
	if live {
		t.sem.proof.liveAll.add("", ref)
	} else {
		t.sem.proof.liveAll.remove("", ref)
	}
	keys, resources, paths, workspaces := map[depKey]bool{}, map[string]uint64{}, map[depKey]bool{}, map[string]bool{}
	for _, id := range p.DependencyIDs {
		d, _ := t.sem.proof.deps.peek(id)
		// FIXED_CONTENT never goes stale, so it is in no live index (DUR-3.1).
		if d.Kind == domain.DependencyFixedContent {
			continue
		}
		k := depKey{resource: d.ResourceID}
		if d.Locator != nil {
			k.path, _ = d.Locator.Key()
		}
		keys[k] = true
		resources[d.ResourceID]++ // live dependency rows (DUR-3.1 policy cap)
		switch d.Kind {
		case domain.DependencyWorkspace:
			workspaces[d.ResourceID] = true
		case domain.DependencyCurrentPath:
			ancestors, err := store.PathAffectKeys(path.Join(d.Locator.BaseDir, d.Locator.Path))
			if err != nil {
				continue // a validated locator always has a canonical path
			}
			for _, a := range ancestors {
				paths[depKey{d.ResourceID, a}] = true
			}
		}
	}
	for k := range keys {
		if live {
			t.sem.proof.liveDeps.add(k, ref)
		} else {
			t.sem.proof.liveDeps.remove(k, ref)
		}
	}
	for k := range paths {
		if live {
			t.sem.proof.liveByPath.add(k, ref)
		} else {
			t.sem.proof.liveByPath.remove(k, ref)
		}
	}
	for r := range workspaces {
		if live {
			t.sem.proof.liveWS.add(r, ref)
		} else {
			t.sem.proof.liveWS.remove(r, ref)
		}
	}
	for r, rows := range resources {
		n, _ := t.sem.proof.dependents.peek(r)
		if live {
			t.sem.proof.liveByRes.add(r, ref)
			t.sem.proof.dependents.put(r, n+rows)
		} else {
			t.sem.proof.liveByRes.remove(r, ref)
			t.sem.proof.dependents.put(r, n-rows)
		}
	}
}

// --- Declarations ---

// InsertObligationDeclaration stores the immutable declaration of an exact
// version, which must restate the version's own binding. A legacy version
// (no declaration kind) only takes an explicit LEGACY_UNKNOWN declaration.
func (t *semTx) InsertObligationDeclaration(d domain.ObligationDeclaration) error {
	if err := t.t.companion("obligation declaration", d.SemanticMeta, d.Validate); err != nil {
		return err
	}
	key := refKey(d.Target)
	if t.r.sem.proof.decls.has(key) || t.r.sem.proof.declIDs.has(d.ID) {
		return immutable("obligation declaration", d.ID)
	}
	o, ok := t.r.obligations.peek(key)
	if !ok {
		return invalid("obligation declaration %s: version is not stored", d.ID)
	}
	if !declarationMatches(o, d) {
		return invalid("obligation declaration %s: binding disagrees with its version", d.ID)
	}
	if d.WorkspaceBinding != nil && !t.r.sem.res.wbindings.has(wbKey{d.WorkspaceBinding.ID, d.WorkspaceBinding.Version}) {
		return invalid("obligation declaration %s: workspace binding is not stored", d.ID)
	}
	if d.GrantID != "" && !t.r.grants.has(d.GrantID) {
		return invalid("obligation declaration %s: grant %s is not stored", d.ID, d.GrantID)
	}
	t.r.sem.proof.decls.put(key, d)
	t.r.sem.proof.declIDs.put(d.ID, true)
	t.t.sequencedWrite(d.Seq)
	return nil
}

func declarationMatches(o domain.ObligationVersion, d domain.ObligationDeclaration) bool {
	if o.SourceItemID != d.SourceItemID {
		return false
	}
	if o.DeclarationKind == "" {
		return d.Binding == domain.BindingLegacy && d.Diagnostic == domain.BindingLegacyUnknown
	}
	return o.DeclarationID == d.ID && o.DeclarationSlot == d.DeclarationSlot && o.ClaimPatternVersion == d.ClaimPatternVersion &&
		samePtr(o.WorkspaceBindingRef, d.WorkspaceBinding) && sameSpec(o.TargetSpec, d.TargetSpec) && samePtr(o.Matcher, d.Matcher) &&
		o.BindingState == d.Binding
}

func sameSpec(a, b *domain.TargetSpec) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ha, errA := a.CanonicalHash()
	hb, errB := b.CanonicalHash()
	return errA == nil && errB == nil && ha == hb
}

func (r semRead) ObligationDeclaration(ref domain.ObligationRef) (domain.ObligationDeclaration, error) {
	if err := r.r.check(); err != nil {
		return domain.ObligationDeclaration{}, err
	}
	d, ok := r.r.sem.proof.decls.get(refKey(ref))
	if !ok {
		return d, notFound("obligation declaration", ref.ObligationID)
	}
	return d, nil
}

func (r semRead) ExactObligation(ref domain.ObligationRef) (domain.ObligationVersion, error) {
	if err := r.r.check(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if ref.SessionID != r.r.sessionID {
		return domain.ObligationVersion{}, notFound("obligation", ref.ObligationID)
	}
	o, ok := r.r.obligations.get(refKey(ref))
	if !ok {
		return o, notFound("obligation", fmt.Sprintf("%s/%d", ref.ObligationID, ref.Version))
	}
	return o, nil
}

// --- Proofs and assertions ---

// InsertApplicabilityProof stores a proof and its dependencies as one
// write. Its ID is the canonical proof identity of its target and
// satisfying transition, which must exist by commit.
func (t *semTx) InsertApplicabilityProof(p domain.ApplicabilityProof, deps []domain.ProofDependency) error {
	if err := t.t.companion("proof", p.SemanticMeta, p.Validate); err != nil {
		return err
	}
	id, err := domain.ApplicabilityProofID(p.Target, p.TransitionID)
	if err != nil {
		return err
	}
	if p.ID != id {
		return invalid("proof %s: ID is not the proof identity of its target and transition", p.ID)
	}
	if t.r.sem.proof.proofs.has(p.ID) {
		return immutable("proof", p.ID)
	}
	o, ok := t.r.obligations.peek(refKey(p.Target))
	if !ok || !sameSpecHash(o.TargetSpec, p.TargetSpecHash) {
		return invalid("proof %s: target is not a stored version with that target specification", p.ID)
	}
	for _, ev := range p.EvidenceIDs {
		it, ok := t.r.items.peek(ev)
		if !ok || !p.Access.Within(it.Access) {
			return invalid("proof %s: evidence %s is not stored within the proof's boundary", p.ID, ev)
		}
	}
	if p.EvidenceCoverageID != "" && !t.r.sem.coverages.has(p.EvidenceCoverageID) {
		return invalid("proof %s: evidence coverage is not stored", p.ID)
	}
	if p.ObservationID != "" && !t.r.sem.res.observations.has(p.ObservationID) {
		return invalid("proof %s: observation is not stored", p.ID)
	}
	if !t.r.sem.res.bindings.has(p.ResourceID) {
		return invalid("proof %s: resource is not registered", p.ID)
	}
	if len(deps) != len(p.DependencyIDs) {
		return invalid("proof %s: dependencies disagree with its dependency list", p.ID)
	}
	for i, d := range deps {
		if err := t.t.own(d.SessionID); err != nil {
			return err
		}
		if err := d.Validate(); err != nil {
			return err
		}
		if err := t.t.fresh("proof dependency "+d.ID, d.Seq); err != nil {
			return err
		}
		if d.ID != p.DependencyIDs[i] || d.ProofID != p.ID || t.r.sem.proof.deps.has(d.ID) {
			return invalid("proof %s: dependency %s is not its listed, unstored dependency", p.ID, d.ID)
		}
		if !t.r.sem.res.bindings.has(d.ResourceID) {
			return invalid("proof %s: dependency resource %s is not registered", p.ID, d.ResourceID)
		}
	}
	t.r.sem.proof.proofs.put(p.ID, p)
	for _, d := range deps {
		t.r.sem.proof.deps.put(d.ID, d)
		t.r.sem.proof.depsByProof.add(p.ID, seqRef{d.Seq, d.ID})
		t.t.semSeqs = append(t.t.semSeqs, d.Seq)
	}
	if o.Current && o.CurrentProofID == p.ID {
		t.t.indexProofDeps(p.ID, true)
	}
	t.t.sequencedWrite(p.Seq)
	t.t.deferCheck(func() error {
		tr, ok := t.r.transitions.peek(p.TransitionID)
		if !ok || tr.ObligationID != p.Target.ObligationID || tr.Version != p.Target.Version || tr.To != domain.ObligationSatisfied || tr.ProofID != p.ID {
			return invalid("proof %s: satisfying transition %s is not stored", p.ID, p.TransitionID)
		}
		if p.AssertionID != "" {
			a, ok := t.r.sem.proof.assertions.peek(p.AssertionID)
			if !ok || a.ProofID != p.ID {
				return invalid("proof %s: assertion %s is not stored for it", p.ID, p.AssertionID)
			}
		}
		return nil
	})
	return nil
}

func sameSpecHash(spec *domain.TargetSpec, hash string) bool {
	if spec == nil {
		return false
	}
	h, err := spec.CanonicalHash()
	return err == nil && h == hash
}

// InsertAssertion stores an explicit assertion record; its transition (and
// resource proof, when RESOURCE_BOUND) must exist by commit.
func (t *semTx) InsertAssertion(a domain.AssertionRecord) error {
	if err := t.t.companion("assertion", a.SemanticMeta, a.Validate); err != nil {
		return err
	}
	if t.r.sem.proof.assertions.has(a.ID) {
		return immutable("assertion", a.ID)
	}
	if !t.r.obligations.has(refKey(a.Target)) {
		return invalid("assertion %s: target version is not stored", a.ID)
	}
	if a.GrantID != "" && !t.r.grants.has(a.GrantID) {
		return invalid("assertion %s: grant is not stored", a.ID)
	}
	if a.EvidenceCoverageID != "" && !t.r.sem.coverages.has(a.EvidenceCoverageID) {
		return invalid("assertion %s: citation coverage is not stored", a.ID)
	}
	t.r.sem.proof.assertions.put(a.ID, a)
	t.t.sequencedWrite(a.Seq)
	t.t.deferCheck(func() error {
		tr, ok := t.r.transitions.peek(a.TransitionID)
		if !ok || tr.ObligationID != a.Target.ObligationID || tr.Version != a.Target.Version || tr.To != domain.ObligationSatisfied || tr.AssertionMode != a.Mode {
			return invalid("assertion %s: transition %s is not its stored satisfying transition", a.ID, a.TransitionID)
		}
		if a.ProofID != "" {
			p, ok := t.r.sem.proof.proofs.peek(a.ProofID)
			if !ok || p.Target != a.Target {
				return invalid("assertion %s: proof %s is not stored for its target", a.ID, a.ProofID)
			}
		}
		return nil
	})
	return nil
}

func (r semRead) ApplicabilityProof(id string) (domain.ApplicabilityProof, error) {
	if err := r.r.check(); err != nil {
		return domain.ApplicabilityProof{}, err
	}
	p, ok := r.r.sem.proof.proofs.get(id)
	if !ok {
		return p, notFound("proof", id)
	}
	return p, nil
}

func (r semRead) Assertion(id string) (domain.AssertionRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.AssertionRecord{}, err
	}
	a, ok := r.r.sem.proof.assertions.get(id)
	if !ok {
		return a, notFound("assertion", id)
	}
	return a, nil
}

func (r semRead) TransitionDetail(transitionID string) (domain.TransitionDetail, error) {
	if err := r.r.check(); err != nil {
		return domain.TransitionDetail{}, err
	}
	d, ok := r.r.sem.proof.details.get(transitionID)
	if !ok {
		return d, notFound("transition detail", transitionID)
	}
	return d, nil
}

func (r semRead) ProofDependencies(proofID string, p store.Page) (store.ResultPage[domain.ProofDependency], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ProofDependency]{}, err
	}
	return page(p, r.r.sem.proof.depsByProof.after(proofID, cursorRef(p.After)), loadAll(&r.r.sem.proof.deps, ident))
}

// CurrentProofsByDependency pages the current proofs a change at pathKey of
// resourceID may invalidate: exact path dependencies plus workspace-level
// dependencies, or every dependency on the resource when pathKey is empty.
func (r semRead) CurrentProofsByDependency(resourceID, pathKey string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	refs := r.r.sem.proof.liveByRes.after(resourceID, cursorRef(p.After))
	if pathKey != "" {
		refs = dedup(mergeAfter(&r.r.sem.proof.liveDeps, []depKey{{resourceID, pathKey}, {resourceID, ""}}, cursorRef(p.After)))
	}
	return page(p, refs, loadAll(&r.r.sem.proof.proofs, ident))
}

// dedup drops consecutive repeats from an ordered reference sequence.
func dedup(refs func(func(seqRef) bool)) func(func(seqRef) bool) {
	return func(yield func(seqRef) bool) {
		var last seqRef
		first := true
		for ref := range refs {
			if !first && ref == last {
				continue
			}
			first, last = false, ref
			if !yield(ref) {
				return
			}
		}
	}
}

func (r semRead) CurrentBoundObligationsBySubject(subjectKey string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ObligationVersion]{}, err
	}
	return pageRefs(p, r.r.sem.proof.bound.after(subjectKey, cursorRef(p.After)), func(ref seqRef) (domain.ObligationVersion, bool) {
		o, ok := r.indexedVersion(ref)
		return o, ok && o.BindingState == domain.BindingBound && o.TargetSubjectKey == subjectKey
	})
}

// ObligationsByTaskOwner pages the current versions whose declared owning
// scope is TURN or TASK of taskID, with no access filter (completion).
func (r semRead) ObligationsByTaskOwner(taskID string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ObligationVersion]{}, err
	}
	return pageRefs(p, r.r.sem.proof.owners.after(taskID, cursorRef(p.After)), func(ref seqRef) (domain.ObligationVersion, bool) {
		o, ok := r.indexedVersion(ref)
		return o, ok && o.Access.TaskID == taskID && (o.Access.Scope == domain.ScopeTask || o.Access.Scope == domain.ScopeTurn)
	})
}

// indexedVersion loads the exact current version an index entry names by
// its (CreatedSeq, ObligationID), not the obligation's latest version
// (DUR-1.13): versions take increasing creation sequences, so the newest
// matching one is found walking back from the latest.
func (r semRead) indexedVersion(ref seqRef) (domain.ObligationVersion, bool) {
	latest, ok := r.r.latest.peek(ref.id)
	if !ok {
		return domain.ObligationVersion{}, false
	}
	for v := latest; v > 0; v-- {
		o, ok := r.r.obligations.peek(obligationKey{ref.id, v})
		if !ok || o.CreatedSeq < ref.seq {
			return domain.ObligationVersion{}, false
		}
		if o.CreatedSeq == ref.seq {
			o, ok = r.r.obligations.get(obligationKey{ref.id, v})
			return o, ok && o.Current
		}
	}
	return domain.ObligationVersion{}, false
}

func (r semRead) TransitionsByVersion(target domain.ObligationRef, p store.Page) (store.ResultPage[domain.ObligationTransition], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ObligationTransition]{}, err
	}
	return page(p, r.r.sem.proof.trByVersion.after(refKey(target), cursorRef(p.After)), loadAll(&r.r.transitions, ident))
}

// Satisfies is the derived SATISFIES view (Q2): one relation per evidence
// item of each proof-backed satisfying transition of the version, in
// (transition Seq, transition/evidence) order. A bare attestation has no
// evidence edge. A relation is visible when both the proof's boundary and
// the evidence item's permit viewer; currentOnly keeps only the version's
// current proof.
func (r semRead) Satisfies(viewer domain.Principal, target domain.ObligationRef, currentOnly bool, p store.Page) (store.ResultPage[domain.SatisfiesRelation], error) {
	var out store.ResultPage[domain.SatisfiesRelation]
	if err := r.r.check(); err != nil {
		return out, err
	}
	if err := viewer.Validate(); err != nil {
		return out, err
	}
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	o, _ := r.r.obligations.peek(refKey(target))
	after := cursorRef(p.After)
	for ref := range r.r.sem.proof.trByVersion.after(refKey(target), seqRef{}) {
		tr, _ := r.r.transitions.peek(ref.id)
		if tr.To != domain.ObligationSatisfied || tr.ProofID == "" {
			continue
		}
		pr, ok := r.r.sem.proof.proofs.peek(tr.ProofID)
		current := o.Current && o.CurrentProofID == pr.ID
		if !ok || currentOnly && !current || !pr.Access.Permits(viewer) {
			continue
		}
		evidence := slices.Clone(pr.EvidenceIDs)
		slices.Sort(evidence)
		for _, ev := range evidence {
			pos := seqRef{tr.Seq, tr.ID + "/" + ev}
			if !after.less(pos) {
				continue
			}
			it, ok := r.r.items.peek(ev)
			if !ok || !it.Access.Permits(viewer) {
				continue
			}
			if len(out.Records) == p.Limit {
				out.More = true
				return out, nil
			}
			out.Records = append(out.Records, domain.SatisfiesRelation{Evidence: domain.ItemContentRef{ItemID: ev, ContentHash: it.ContentHash},
				Target: target, TransitionID: tr.ID, ProofID: pr.ID, Current: current, Access: pr.Access})
			out.Next = store.Cursor{Seq: pos.seq, ID: pos.id}
		}
	}
	return out, nil
}

// --- Transitions ---

// checkTransition applies the shared transition rules to tr against the
// stored version: CAS, currentness and the From status. It returns the
// current and next version without writing.
func (t *tx) checkTransition(tr domain.ObligationTransition, expectedRevision uint64) (cur, next domain.ObligationVersion, err error) {
	if err := t.own(tr.SessionID); err != nil {
		return cur, next, err
	}
	if err := tr.Validate(); err != nil {
		return cur, next, err
	}
	if err := t.fresh("obligation transition "+tr.ID, tr.Seq); err != nil {
		return cur, next, err
	}
	if t.transitions.has(tr.ID) {
		return cur, next, fmt.Errorf("obligation transition %s: %w", tr.ID, domain.ErrImmutable)
	}
	key := obligationKey{tr.ObligationID, tr.Version}
	cur, ok := t.obligations.peek(key)
	if !ok {
		return cur, next, notFound("obligation", fmt.Sprintf("%s/%d", tr.ObligationID, tr.Version))
	}
	if cur.Revision != expectedRevision {
		return cur, next, fmt.Errorf("obligation %s/%d: revision %d, expected %d: %w",
			tr.ObligationID, tr.Version, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if !cur.Current {
		return cur, next, fmt.Errorf("obligation %s/%d: retired versions do not transition: %w", tr.ObligationID, tr.Version, domain.ErrInvalidTransition)
	}
	if cur.Status != tr.From {
		return cur, next, fmt.Errorf("obligation %s/%d: transition from %s but status is %s: %w",
			tr.ObligationID, tr.Version, tr.From, cur.Status, domain.ErrInvalidTransition)
	}
	next = cur.Clone()
	next.Status = tr.To
	next.EvidenceIDs = nil
	if tr.To == domain.ObligationSatisfied {
		next.EvidenceIDs = slices.Clone(tr.EvidenceIDs)
	}
	next.Revision++
	return cur, next, nil
}

// writeTransition stores a checked transition and its version.
func (t *tx) writeTransition(tr domain.ObligationTransition, cur, next domain.ObligationVersion) {
	key := obligationKey{tr.ObligationID, tr.Version}
	t.transitions.put(tr.ID, tr)
	t.sem.proof.trByVersion.add(key, seqRef{tr.Seq, tr.ID})
	t.obligations.put(key, next)
	t.noteObligation(cur, next)
	t.markSequenced()
}

// AppendSemanticObligationTransition appends a Phase 3 transition with its
// immutable detail and updates status, evidence and the proof/assertion
// cache atomically. The detail restates the transition's cause, proof,
// prior proof and historical authorization; a prior proof is the version's
// current one; named proofs and assertions must exist by commit, other
// named records now.
func (t *semTx) AppendSemanticObligationTransition(tr domain.ObligationTransition, d domain.TransitionDetail, expectedRevision uint64) (domain.ObligationVersion, error) {
	if tr.Cause == "" {
		return domain.ObligationVersion{}, invalid("transition %s: a semantic transition requires its cause", tr.ID)
	}
	if err := t.t.companion("transition detail", d.SemanticMeta, d.Validate); err != nil {
		return domain.ObligationVersion{}, err
	}
	ref := domain.ObligationRef{SessionID: tr.SessionID, ObligationID: tr.ObligationID, Version: tr.Version}
	if d.TransitionID != tr.ID || d.Target != ref || d.Cause != tr.Cause || d.ProofID != tr.ProofID || d.PreviousProofID != tr.PriorProofID ||
		!samePtr(d.OriginAuthorization, tr.OriginAuthorizationRef) {
		return domain.ObligationVersion{}, invalid("transition %s: detail does not restate its transition", tr.ID)
	}
	if t.r.sem.proof.details.has(tr.ID) {
		return domain.ObligationVersion{}, immutable("transition detail", tr.ID)
	}
	cur, next, err := t.t.checkTransition(tr, expectedRevision)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	if tr.PriorProofID != "" && tr.PriorProofID != cur.CurrentProofID {
		return domain.ObligationVersion{}, invalid("transition %s: prior proof is not the version's current proof", tr.ID)
	}
	if origin := tr.OriginAuthorizationRef; origin != nil {
		prior, ok := t.r.transitions.peek(origin.TransitionID)
		if !ok || prior.ObligationID != tr.ObligationID || prior.Version != tr.Version || prior.Seq != origin.Seq ||
			origin.GrantID != "" && !t.r.grants.has(origin.GrantID) {
			return domain.ObligationVersion{}, invalid("transition %s: historical authorization is not a stored transition of its target", tr.ID)
		}
	}
	if tr.CauseRecordID != "" && !t.r.sem.res.updates.has(tr.CauseRecordID) && !t.r.sem.res.observations.has(tr.CauseRecordID) {
		return domain.ObligationVersion{}, invalid("transition %s: cause record is not stored", tr.ID)
	}
	if d.ResourceUpdateID != "" && !t.r.sem.res.updates.has(d.ResourceUpdateID) || d.ObservationID != "" && !t.r.sem.res.observations.has(d.ObservationID) {
		return domain.ObligationVersion{}, invalid("transition %s: detail names an unstored cause", tr.ID)
	}
	if err := store.ValidateSatisfactionBacking(tr, d); err != nil {
		return domain.ObligationVersion{}, err
	}
	next.CurrentProofID, next.CurrentAssertionID = "", ""
	if tr.To == domain.ObligationSatisfied {
		next.CurrentProofID, next.CurrentAssertionID = tr.ProofID, d.AssertionID
	}
	if err := next.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	t.t.writeTransition(tr, cur, next)
	t.r.sem.proof.details.put(tr.ID, d)
	t.t.semSeqs = append(t.t.semSeqs, d.Seq)
	t.t.deferCheck(func() error {
		if tr.ProofID != "" {
			p, ok := t.r.sem.proof.proofs.peek(tr.ProofID)
			if !ok || p.TransitionID != tr.ID || p.Target != ref {
				return invalid("transition %s: proof %s is not stored for it", tr.ID, tr.ProofID)
			}
		}
		if d.AssertionID != "" {
			a, ok := t.r.sem.proof.assertions.peek(d.AssertionID)
			if !ok || a.TransitionID != tr.ID || a.Target != ref || a.Mode != tr.AssertionMode || a.ProofID != tr.ProofID {
				return invalid("transition %s: assertion %s is not stored for it", tr.ID, d.AssertionID)
			}
		}
		if err := t.r.checkProofNotStale(ref, tr.ProofID); err != nil {
			return err
		}
		// The A5 commit guard refuses a SATISFIED write resting on a
		// derived-invalid proof (K1 A5, semantic_k1.go).
		return t.t.checkProofDerivedValid(t.r, ref, tr.ProofID)
	})
	return next.Clone(), nil
}

// checkProofNotStale is the commit-time half of G1/H1 (INV-16, P3-16/22):
// if the version still rests on proofID at commit and that proof rests on
// an observation, no partition that can outrank it may have a complete
// PASS or FAIL from a newer run (store.ProofRankPartitions).
func (r *readTx) checkProofNotStale(ref domain.ObligationRef, proofID string) error {
	if proofID == "" {
		return nil
	}
	o, ok := r.obligations.peek(refKey(ref))
	if !ok || o.Status != domain.ObligationSatisfied || o.CurrentProofID != proofID {
		return nil
	}
	p, _ := r.sem.proof.proofs.peek(proofID)
	if p.ObservationID == "" {
		return nil
	}
	obs, ok := r.sem.res.observations.peek(p.ObservationID)
	if !ok {
		return invalid("proof %s: observation %s is not stored", proofID, p.ObservationID)
	}
	run, ok := r.sem.res.runs.peek(obs.RunID)
	if !ok {
		return invalid("proof %s: run %s is not stored", proofID, obs.RunID)
	}
	// Ordering is by run ordinal over every complete result, whatever its
	// applicability (H1); a partition that does not cover o never counts.
	for _, part := range store.ProofRankPartitions(run, o) {
		if hw, _ := r.sem.res.highWater.peek(subjectKey{run.SubjectKey, part.TaskID, part.Access}); hw > run.Ordinal {
			return fmt.Errorf("proof %s: run ordinal %d is older than the subject's high-water mark %d: %w",
				proofID, run.Ordinal, hw, domain.ErrInvalidTransition)
		}
	}
	return nil
}

// SetObligationMaterialization records an audited materialization
// exception on a current version: CAS, a real change, and an audit event
// for the obligation. It changes rendering only.
func (t *semTx) SetObligationMaterialization(target domain.ObligationRef, disabled bool, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := t.t.own(target.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	key := refKey(target)
	cur, ok := t.r.obligations.peek(key)
	if !ok {
		return domain.ObligationVersion{}, notFound("obligation", target.ObligationID)
	}
	if cur.Revision != expectedRevision {
		return domain.ObligationVersion{}, conflict("obligation %s/%d: revision %d, expected %d", target.ObligationID, target.Version, cur.Revision, expectedRevision)
	}
	if err := t.t.checkTargetEvent(event, domain.TargetObligation, target.ObligationID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if !cur.Current || cur.MaterializationDisabled == disabled {
		return domain.ObligationVersion{}, transition("obligation %s/%d: no materialization change", target.ObligationID, target.Version)
	}
	next := cur.Clone()
	next.MaterializationDisabled, next.Revision = disabled, expectedRevision+1
	t.r.obligations.put(key, next)
	t.t.putLifecycle(event)
	t.t.markSequenced()
	return next.Clone(), nil
}

// ObligationTransition implements store.ProofReader: one keyed lookup.
func (r semRead) ObligationTransition(id string) (domain.ObligationTransition, error) {
	if err := r.r.check(); err != nil {
		return domain.ObligationTransition{}, err
	}
	tr, ok := r.r.transitions.get(id)
	if !ok {
		return tr, notFound("obligation transition", id)
	}
	return tr, nil
}

// LiveProofsByPath implements store.ProofReader (DUR-3.1).
func (r semRead) LiveProofsByPath(resourceID, path string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	if _, err := store.PathAffectKeys(path); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	return page(p, r.r.sem.proof.liveByPath.after(depKey{resourceID, path}, cursorRef(p.After)), loadAll(&r.r.sem.proof.proofs, ident))
}

// LiveWorkspaceProofs implements store.ProofReader (DUR-3.1).
func (r semRead) LiveWorkspaceProofs(resourceID string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	return page(p, r.r.sem.proof.liveWS.after(resourceID, cursorRef(p.After)), loadAll(&r.r.sem.proof.proofs, ident))
}

// LiveProofDependents implements store.ProofReader (DUR-3.1).
func (r semRead) LiveProofDependents(resourceID string) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	n, _ := r.r.sem.proof.dependents.get(resourceID)
	return n, nil
}
