package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// obligationDeclarationRow is the declaration of one exact obligation
// version, keyed by (obligation, version) (P3-12).
type obligationDeclarationRow struct {
	SessionID    string
	ObligationID string
	Version      int
	Declaration  domain.ObligationDeclaration
}

// SemanticSeq places the row's sequence in the TargetCall sharing check
// (P3-1, SPEC-1.4).
func (r obligationDeclarationRow) SemanticSeq() uint64 { return r.Declaration.Seq }

// noteLiveProof keeps lookup_live_dependency in step with a version write:
// the dependencies of a version's current proof are live exactly while the
// version is current and names that proof.
func (t *transaction) noteLiveProof(before, after domain.ObligationVersion) error {
	oldProof, newProof := "", ""
	if before.Current {
		oldProof = before.CurrentProofID
	}
	if after.Current {
		newProof = after.CurrentProofID
	}
	if oldProof == newProof {
		return nil
	}
	if err := t.indexProofDeps(oldProof, false); err != nil {
		return err
	}
	return t.indexProofDeps(newProof, true)
}

// indexProofDeps adds or removes a stored proof's dependencies in the live
// index; a proof not stored yet is indexed when it is inserted.
func (t *transaction) indexProofDeps(proofID string, live bool) error {
	if proofID == "" {
		return nil
	}
	var p domain.ApplicabilityProof
	if err := t.get("proof", proofID, 0, &p); errors.Is(err, domain.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	// The audit worker's index holds every live proof — the current proof
	// of a current obligation version — FIXED_CONTENT-only ones included
	// (K1 A4, K1-api.2).
	if err := t.noteLiveProofID(p, live); err != nil {
		return err
	}
	seen, paths, resources := map[[2]string]bool{}, map[[2]string]bool{}, map[string]int{}
	for _, id := range p.DependencyIDs {
		var d domain.ProofDependency
		if err := t.get("proof_dependency", id, 0, &d); err != nil {
			return integrityIfMissing(err, "proof dependency")
		}
		// FIXED_CONTENT never goes stale, so it is in no live index (DUR-3.1).
		if d.Kind == domain.DependencyFixedContent {
			continue
		}
		resources[d.ResourceID]++ // live dependency rows (DUR-3.1 policy cap)
		switch d.Kind {
		case domain.DependencyWorkspace:
			paths[[2]string{d.ResourceID, liveWorkspaceKey}] = true
		case domain.DependencyCurrentPath:
			ancestors, err := store.PathAffectKeys(path.Join(d.Locator.BaseDir, d.Locator.Path))
			if err != nil {
				return err
			}
			for _, a := range ancestors {
				paths[[2]string{d.ResourceID, livePathKey(a)}] = true
			}
		}
		k := [2]string{d.ResourceID, ""}
		if d.Locator != nil {
			k[1], _ = d.Locator.Key()
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		q := "DELETE FROM lookup_live_dependency WHERE session_id=? AND resource_id=? AND path_key=? AND seq=? AND proof_id=?"
		if live {
			q = "INSERT INTO lookup_live_dependency(session_id,resource_id,path_key,seq,proof_id) VALUES(?,?,?,?,?)"
		}
		if _, err := t.conn.ExecContext(t.ctx, q, t.session, k[0], k[1], p.Seq, p.ID); err != nil {
			return err
		}
	}
	for k := range paths {
		q := "DELETE FROM lookup_live_proof_path WHERE session_id=? AND resource_id=? AND key=? AND seq=? AND proof_id=?"
		if live {
			q = "INSERT INTO lookup_live_proof_path(session_id,resource_id,key,seq,proof_id) VALUES(?,?,?,?,?)"
		}
		if _, err := t.conn.ExecContext(t.ctx, q, t.session, k[0], k[1], p.Seq, p.ID); err != nil {
			return err
		}
	}
	for r, rows := range resources {
		delta := rows
		if !live {
			delta = -rows
		}
		if _, err := t.conn.ExecContext(t.ctx, `INSERT INTO lookup_live_dependents(session_id,resource_id,dependents) VALUES(?,?,?)
ON CONFLICT(session_id,resource_id) DO UPDATE SET dependents = dependents + excluded.dependents`, t.session, r, delta); err != nil {
			return err
		}
	}
	return nil
}

// Keys of migration 0045's lookup_live_proof_path.
const liveWorkspaceKey = "ws"

func livePathKey(p string) string { return "path:" + hex.EncodeToString([]byte(p)) }

// liveProofPathPage is the keyset page read of lookup_live_proof_path.
const liveProofPathPage = "SELECT seq, proof_id FROM lookup_live_proof_path WHERE session_id=? AND resource_id=? AND key=? AND (seq, proof_id) > (?, ?) ORDER BY seq, proof_id LIMIT ?"

// liveProofPage pages one key of lookup_live_proof_path.
func (s semRead) liveProofPage(resourceID, key string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	t := s.t
	var out store.ResultPage[domain.ApplicabilityProof]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	rows, err := t.query(liveProofPathPage, t.session, resourceID, key, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var seq uint64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, id := range ids {
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		pr, err := s.ApplicabilityProof(id)
		if err != nil {
			return out, integrityIfMissing(err, "proof")
		}
		out.Records = append(out.Records, pr)
		out.Next = store.Cursor{Seq: pr.Seq, ID: pr.ID}
	}
	return out, nil
}

// --- Declarations (rules as in the memory store) ---

func (s semTx) InsertObligationDeclaration(d domain.ObligationDeclaration) error {
	t := s.t
	if err := t.companion(d.SemanticMeta, d.Validate); err != nil {
		return err
	}
	var prior obligationDeclarationRow
	if err := t.get("obligation_declaration", d.Target.ObligationID, int(d.Target.Version), &prior); err == nil {
		return immutable("obligation declaration", d.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err := t.getWhere("obligation_declaration", "f_declaration_semantic_meta_id=?", &prior, d.ID); err == nil {
		return immutable("obligation declaration", d.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", d.Target.ObligationID, int(d.Target.Version), &o); err != nil {
		return notStored(err, "obligation declaration %s: version is not stored", d.ID)
	}
	if !declarationMatches(o, d) {
		return invalid("obligation declaration %s: binding disagrees with its version", d.ID)
	}
	if d.WorkspaceBinding != nil {
		var b domain.WorkspaceBinding
		if err := t.get("workspace_binding", d.WorkspaceBinding.ID, int(d.WorkspaceBinding.Version), &b); err != nil {
			return notStored(err, "obligation declaration %s: workspace binding is not stored", d.ID)
		}
	}
	if d.GrantID != "" {
		if ok, err := t.exists("grant", d.GrantID); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "obligation declaration %s: grant %s is not stored", d.ID, d.GrantID))
		}
	}
	return t.put("obligation_declaration", d.Target.ObligationID, int(d.Target.Version),
		obligationDeclarationRow{SessionID: t.session, ObligationID: d.Target.ObligationID, Version: int(d.Target.Version), Declaration: d}, false)
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

func sameSpecHash(spec *domain.TargetSpec, hash string) bool {
	if spec == nil {
		return false
	}
	h, err := spec.CanonicalHash()
	return err == nil && h == hash
}

func (s semRead) ObligationDeclaration(ref domain.ObligationRef) (domain.ObligationDeclaration, error) {
	var row obligationDeclarationRow
	return row.Declaration, s.t.get("obligation_declaration", ref.ObligationID, int(ref.Version), &row)
}

func (s semRead) ExactObligation(ref domain.ObligationRef) (domain.ObligationVersion, error) {
	var o domain.ObligationVersion
	if ref.SessionID != s.t.session {
		return o, domain.ErrNotFound
	}
	return o, s.t.get("obligation", ref.ObligationID, int(ref.Version), &o)
}

// --- Proofs and assertions ---

func (s semTx) InsertApplicabilityProof(p domain.ApplicabilityProof, deps []domain.ProofDependency) error {
	t := s.t
	if err := t.companion(p.SemanticMeta, p.Validate); err != nil {
		return err
	}
	id, err := domain.ApplicabilityProofID(p.Target, p.TransitionID)
	if err != nil {
		return err
	}
	if p.ID != id {
		return invalid("proof %s: ID is not the proof identity of its target and transition", p.ID)
	}
	if ok, err := t.exists("proof", p.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "proof", p.ID))
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", p.Target.ObligationID, int(p.Target.Version), &o); err != nil || !sameSpecHash(o.TargetSpec, p.TargetSpecHash) {
		return notStored(errors.Join(err, domain.ErrNotFound), "proof %s: target is not a stored version with that target specification", p.ID)
	}
	for _, ev := range p.EvidenceIDs {
		it, err := t.loadItem(ev, false)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err != nil || !p.Access.Within(it.Access) {
			return invalid("proof %s: evidence %s is not stored within the proof's boundary", p.ID, ev)
		}
	}
	for _, ref := range []struct{ kind, id, what string }{{"coverage", p.EvidenceCoverageID, "evidence coverage"}, {"observation", p.ObservationID, "observation"}, {"resource_binding", p.ResourceID, "resource"}} {
		if ref.id == "" {
			continue
		}
		if ok, err := t.exists(ref.kind, ref.id); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "proof %s: %s is not stored", p.ID, ref.what))
		}
	}
	if len(deps) != len(p.DependencyIDs) {
		return invalid("proof %s: dependencies disagree with its dependency list", p.ID)
	}
	for i, d := range deps {
		if err := t.companion(d.SemanticMeta, d.Validate); err != nil {
			return err
		}
		if d.ID != p.DependencyIDs[i] || d.ProofID != p.ID {
			return invalid("proof %s: dependency %s is not its listed dependency", p.ID, d.ID)
		}
		if ok, err := t.exists("proof_dependency", d.ID); err != nil || ok {
			return errors.Join(err, invalidIf(ok, "proof %s: dependency %s is already stored", p.ID, d.ID))
		}
		if ok, err := t.exists("resource_binding", d.ResourceID); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "proof %s: dependency resource %s is not registered", p.ID, d.ResourceID))
		}
	}
	err = t.atomic(func() error {
		if err := t.put("proof", p.ID, 0, p, false); err != nil {
			return err
		}
		for _, d := range deps {
			if err := t.put("proof_dependency", d.ID, 0, d, false); err != nil {
				return err
			}
		}
		if o.Current && o.CurrentProofID == p.ID {
			return t.indexProofDeps(p.ID, true)
		}
		return nil
	})
	if err != nil {
		return err
	}
	t.deferCheck(func() error {
		var tr domain.ObligationTransition
		if err := t.get("obligation_transition", p.TransitionID, 0, &tr); err != nil || tr.ObligationID != p.Target.ObligationID || tr.Version != p.Target.Version ||
			tr.To != domain.ObligationSatisfied || tr.ProofID != p.ID {
			return notStored(errors.Join(err, domain.ErrNotFound), "proof %s: satisfying transition %s is not stored", p.ID, p.TransitionID)
		}
		if p.AssertionID != "" {
			var a domain.AssertionRecord
			if err := t.get("assertion", p.AssertionID, 0, &a); err != nil || a.ProofID != p.ID {
				return notStored(errors.Join(err, domain.ErrNotFound), "proof %s: assertion %s is not stored for it", p.ID, p.AssertionID)
			}
		}
		return nil
	})
	return nil
}

func (s semTx) InsertAssertion(a domain.AssertionRecord) error {
	t := s.t
	if err := t.companion(a.SemanticMeta, a.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("assertion", a.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "assertion", a.ID))
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", a.Target.ObligationID, int(a.Target.Version), &o); err != nil {
		return notStored(err, "assertion %s: target version is not stored", a.ID)
	}
	for _, ref := range []struct{ kind, id, what string }{{"grant", a.GrantID, "grant"}, {"coverage", a.EvidenceCoverageID, "citation coverage"}} {
		if ref.id == "" {
			continue
		}
		if ok, err := t.exists(ref.kind, ref.id); err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "assertion %s: %s is not stored", a.ID, ref.what))
		}
	}
	if err := t.put("assertion", a.ID, 0, a, false); err != nil {
		return err
	}
	t.deferCheck(func() error {
		var tr domain.ObligationTransition
		if err := t.get("obligation_transition", a.TransitionID, 0, &tr); err != nil || tr.ObligationID != a.Target.ObligationID || tr.Version != a.Target.Version ||
			tr.To != domain.ObligationSatisfied || tr.AssertionMode != a.Mode {
			return notStored(errors.Join(err, domain.ErrNotFound), "assertion %s: transition %s is not its stored satisfying transition", a.ID, a.TransitionID)
		}
		if a.ProofID != "" {
			var p domain.ApplicabilityProof
			if err := t.get("proof", a.ProofID, 0, &p); err != nil || p.Target != a.Target {
				return notStored(errors.Join(err, domain.ErrNotFound), "assertion %s: proof %s is not stored for its target", a.ID, a.ProofID)
			}
		}
		return nil
	})
	return nil
}

func (s semRead) ApplicabilityProof(id string) (domain.ApplicabilityProof, error) {
	var p domain.ApplicabilityProof
	return p, s.t.get("proof", id, 0, &p)
}

func (s semRead) Assertion(id string) (domain.AssertionRecord, error) {
	var a domain.AssertionRecord
	return a, s.t.get("assertion", id, 0, &a)
}

func (s semRead) TransitionDetail(transitionID string) (domain.TransitionDetail, error) {
	var d domain.TransitionDetail
	return d, s.t.get("transition_detail", transitionID, 0, &d)
}

func (s semRead) ProofDependencies(proofID string, p store.Page) (store.ResultPage[domain.ProofDependency], error) {
	return pageQuery[domain.ProofDependency](s.t, "proof_dependency", "f_proof_id=?", []any{proofID}, "f_seq", p, false, nil)
}

// CurrentProofsByDependency: see the memory store.
func (s semRead) CurrentProofsByDependency(resourceID, pathKey string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	t := s.t
	var out store.ResultPage[domain.ApplicabilityProof]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	q := "SELECT DISTINCT seq, proof_id FROM lookup_live_dependency WHERE session_id=? AND resource_id=?"
	args := []any{t.session, resourceID}
	if pathKey != "" {
		q += " AND path_key IN (?,'')"
		args = append(args, pathKey)
	}
	q += " AND (seq, proof_id) > (?, ?) ORDER BY seq, proof_id LIMIT ?"
	args = append(args, p.After.Seq, p.After.ID, p.Limit+1)
	rows, err := t.query(q, args...)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var seq uint64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, id := range ids {
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		pr, err := s.ApplicabilityProof(id)
		if err != nil {
			return out, integrityIfMissing(err, "proof")
		}
		out.Records = append(out.Records, pr)
		out.Next = store.Cursor{Seq: pr.Seq, ID: pr.ID}
	}
	return out, nil
}

func (s semRead) CurrentBoundObligationsBySubject(subjectKey string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return pageQuery[domain.ObligationVersion](s.t, "obligation", "f_target_subject_key=? AND f_current=1 AND f_binding_state='BOUND'", []any{subjectKey}, "f_created_seq", p, false, nil)
}

func (s semRead) ObligationsByTaskOwner(taskID string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return pageQuery[domain.ObligationVersion](s.t, "obligation", "f_access_task_id=? AND f_current=1 AND f_access_scope IN ('TASK','TURN')", []any{taskID}, "f_created_seq", p, false, nil)
}

func (s semRead) TransitionsByVersion(target domain.ObligationRef, p store.Page) (store.ResultPage[domain.ObligationTransition], error) {
	return pageQuery[domain.ObligationTransition](s.t, "obligation_transition", "f_obligation_id=? AND f_version=?", []any{target.ObligationID, target.Version}, "f_seq", p, false, nil)
}

// Satisfies is the derived SATISFIES view; see the memory store.
func (s semRead) Satisfies(viewer domain.Principal, target domain.ObligationRef, currentOnly bool, p store.Page) (store.ResultPage[domain.SatisfiesRelation], error) {
	t := s.t
	var out store.ResultPage[domain.SatisfiesRelation]
	if err := viewer.Validate(); err != nil {
		return out, err
	}
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", target.ObligationID, int(target.Version), &o); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return out, err
	}
	var trs []domain.ObligationTransition
	for cursor := (store.Cursor{}); ; {
		pg, err := s.TransitionsByVersion(target, store.Page{After: cursor, Limit: 256})
		if err != nil {
			return out, err
		}
		trs = append(trs, pg.Records...)
		if !pg.More {
			break
		}
		cursor = pg.Next
	}
	after := p.After
	for _, tr := range trs {
		if tr.To != domain.ObligationSatisfied || tr.ProofID == "" {
			continue
		}
		pr, err := s.ApplicabilityProof(tr.ProofID)
		if err != nil {
			continue
		}
		current := o.Current && o.CurrentProofID == pr.ID
		if currentOnly && !current || !pr.Access.Permits(viewer) {
			continue
		}
		evidence := slices.Clone(pr.EvidenceIDs)
		slices.Sort(evidence)
		for _, ev := range evidence {
			pos := store.Cursor{Seq: tr.Seq, ID: tr.ID + "/" + ev}
			if pos.Seq < after.Seq || pos.Seq == after.Seq && pos.ID <= after.ID {
				continue
			}
			it, err := t.loadItem(ev, false)
			if err != nil || !it.Access.Permits(viewer) {
				continue
			}
			if len(out.Records) == p.Limit {
				out.More = true
				return out, nil
			}
			out.Records = append(out.Records, domain.SatisfiesRelation{Evidence: domain.ItemContentRef{ItemID: ev, ContentHash: it.ContentHash},
				Target: target, TransitionID: tr.ID, ProofID: pr.ID, Current: current, Access: pr.Access})
			out.Next = pos
		}
	}
	return out, nil
}

// --- Transitions ---

// AppendSemanticObligationTransition: see the memory store for the rules.
func (s semTx) AppendSemanticObligationTransition(tr domain.ObligationTransition, d domain.TransitionDetail, expectedRevision uint64) (domain.ObligationVersion, error) {
	t := s.t
	if tr.Cause == "" {
		return domain.ObligationVersion{}, invalid("transition %s: a semantic transition requires its cause", tr.ID)
	}
	if err := t.companion(d.SemanticMeta, d.Validate); err != nil {
		return domain.ObligationVersion{}, err
	}
	ref := domain.ObligationRef{SessionID: tr.SessionID, ObligationID: tr.ObligationID, Version: tr.Version}
	if d.TransitionID != tr.ID || d.Target != ref || d.Cause != tr.Cause || d.ProofID != tr.ProofID || d.PreviousProofID != tr.PriorProofID ||
		!samePtr(d.OriginAuthorization, tr.OriginAuthorizationRef) {
		return domain.ObligationVersion{}, invalid("transition %s: detail does not restate its transition", tr.ID)
	}
	if ok, err := t.exists("transition_detail", tr.ID); err != nil || ok {
		return domain.ObligationVersion{}, errors.Join(err, immutableIf(ok, "transition detail", tr.ID))
	}
	old, next, err := t.checkTransition(tr, expectedRevision)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	if tr.PriorProofID != "" && tr.PriorProofID != old.CurrentProofID {
		return domain.ObligationVersion{}, invalid("transition %s: prior proof is not the version's current proof", tr.ID)
	}
	if origin := tr.OriginAuthorizationRef; origin != nil {
		var prior domain.ObligationTransition
		if err := t.get("obligation_transition", origin.TransitionID, 0, &prior); err != nil || prior.ObligationID != tr.ObligationID || prior.Version != tr.Version || prior.Seq != origin.Seq {
			return domain.ObligationVersion{}, notStored(errors.Join(err, domain.ErrNotFound), "transition %s: historical authorization is not a stored transition of its target", tr.ID)
		}
		if origin.GrantID != "" {
			if ok, err := t.exists("grant", origin.GrantID); err != nil || !ok {
				return domain.ObligationVersion{}, errors.Join(err, invalidIf(!ok, "transition %s: historical grant is not stored", tr.ID))
			}
		}
	}
	if tr.CauseRecordID != "" {
		upd, err := t.exists("resource_update", tr.CauseRecordID)
		if err != nil {
			return domain.ObligationVersion{}, err
		}
		obs, err := t.exists("observation", tr.CauseRecordID)
		if err != nil {
			return domain.ObligationVersion{}, err
		}
		if !upd && !obs {
			return domain.ObligationVersion{}, invalid("transition %s: cause record is not stored", tr.ID)
		}
	}
	for _, ref := range []struct{ kind, id string }{{"resource_update", d.ResourceUpdateID}, {"observation", d.ObservationID}} {
		if ref.id == "" {
			continue
		}
		if ok, err := t.exists(ref.kind, ref.id); err != nil || !ok {
			return domain.ObligationVersion{}, errors.Join(err, invalidIf(!ok, "transition %s: detail names an unstored cause", tr.ID))
		}
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
	err = t.atomic(func() error {
		if err := t.writeTransition(tr, old, next); err != nil {
			return err
		}
		return t.put("transition_detail", tr.ID, 0, d, false)
	})
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	t.deferCheck(func() error {
		if tr.ProofID != "" {
			var p domain.ApplicabilityProof
			if err := t.get("proof", tr.ProofID, 0, &p); err != nil || p.TransitionID != tr.ID || p.Target != ref {
				return notStored(errors.Join(err, domain.ErrNotFound), "transition %s: proof %s is not stored for it", tr.ID, tr.ProofID)
			}
		}
		if d.AssertionID != "" {
			var a domain.AssertionRecord
			if err := t.get("assertion", d.AssertionID, 0, &a); err != nil || a.TransitionID != tr.ID || a.Target != ref || a.Mode != tr.AssertionMode || a.ProofID != tr.ProofID {
				return notStored(errors.Join(err, domain.ErrNotFound), "transition %s: assertion %s is not stored for it", tr.ID, d.AssertionID)
			}
		}
		return t.checkProofNotStale(ref, tr.ProofID)
	})
	return next.Clone(), nil
}

// SetObligationMaterialization: see the memory store.
func (s semTx) SetObligationMaterialization(target domain.ObligationRef, disabled bool, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	t := s.t
	if err := t.checkSession(target.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	var cur domain.ObligationVersion
	if err := t.get("obligation", target.ObligationID, int(target.Version), &cur); err != nil {
		return domain.ObligationVersion{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.ObligationVersion{}, conflict("obligation %s/%d: revision %d, expected %d", target.ObligationID, target.Version, cur.Revision, expectedRevision)
	}
	if event.TargetKind != domain.TargetObligation || event.TargetID != target.ObligationID {
		return domain.ObligationVersion{}, invalid("obligation %s: materialization audit targets another record", target.ObligationID)
	}
	if err := event.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSession(event.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.checkSeq(event.Seq); err != nil {
		return domain.ObligationVersion{}, err
	}
	if !cur.Current || cur.MaterializationDisabled == disabled {
		return domain.ObligationVersion{}, transition("obligation %s/%d: no materialization change", target.ObligationID, target.Version)
	}
	next := cur.Clone()
	next.MaterializationDisabled, next.Revision = disabled, expectedRevision+1
	err := t.atomic(func() error {
		if err := t.put("obligation", target.ObligationID, int(target.Version), next, true); err != nil {
			return err
		}
		return t.AppendLifecycleEvent(event)
	})
	return next.Clone(), err
}

// checkProofNotStale is the commit-time half of G1/H1 (INV-16, P3-16/22):
// if the version still rests on proofID at commit and that proof rests on
// an observation, no partition that can outrank it may have a complete
// PASS or FAIL from a newer run (store.ProofRankPartitions).
func (t *transaction) checkProofNotStale(ref domain.ObligationRef, proofID string) error {
	if proofID == "" {
		return nil
	}
	var o domain.ObligationVersion
	if err := t.get("obligation", ref.ObligationID, int(ref.Version), &o); err != nil {
		return err
	}
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID != proofID {
		return nil
	}
	var p domain.ApplicabilityProof
	if err := t.get("proof", proofID, 0, &p); errors.Is(err, domain.ErrNotFound) || err == nil && p.ObservationID == "" {
		return nil // a missing proof is the other deferred check's error, as in memory
	} else if err != nil {
		return err
	}
	var obs domain.ObservationRecord
	if err := t.get("observation", p.ObservationID, 0, &obs); err != nil {
		return notStored(err, "proof %s: observation %s is not stored", proofID, p.ObservationID)
	}
	var run domain.ObservationRun
	if err := t.get("observation_run", obs.RunID, 0, &run); err != nil {
		return notStored(err, "proof %s: run %s is not stored", proofID, obs.RunID)
	}
	// Ordering is by run ordinal over every complete result, whatever its
	// applicability (H1); a partition that does not cover o never counts.
	for _, part := range store.ProofRankPartitions(run, o) {
		hw, err := semRead{t}.SubjectHighWater(run.SubjectKey, part.TaskID, part.Access)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if hw > run.Ordinal {
			return fmt.Errorf("proof %s: run ordinal %d is older than the subject's high-water mark %d: %w",
				proofID, run.Ordinal, hw, domain.ErrInvalidTransition)
		}
	}
	return nil
}

// ObligationTransition implements store.ProofReader: one primary-key read.
func (s semRead) ObligationTransition(id string) (domain.ObligationTransition, error) {
	var tr domain.ObligationTransition
	return tr, s.t.get("obligation_transition", id, 0, &tr)
}

// LiveProofsByPath implements store.ProofReader over migration 0045 (DUR-3.1).
func (s semRead) LiveProofsByPath(resourceID, path string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	if _, err := store.PathAffectKeys(path); err != nil {
		return store.ResultPage[domain.ApplicabilityProof]{}, err
	}
	return s.liveProofPage(resourceID, livePathKey(path), p)
}

// LiveWorkspaceProofs implements store.ProofReader over migration 0045 (DUR-3.1).
func (s semRead) LiveWorkspaceProofs(resourceID string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	return s.liveProofPage(resourceID, liveWorkspaceKey, p)
}

// LiveProofDependents implements store.ProofReader over migration 0045 (DUR-3.1).
func (s semRead) LiveProofDependents(resourceID string) (uint64, error) {
	var n int64
	err := s.t.conn.QueryRowContext(s.t.ctx, "SELECT dependents FROM lookup_live_dependents WHERE session_id=? AND resource_id=?", s.t.session, resourceID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: negative live dependent count for %s", domain.ErrIntegrity, resourceID)
	}
	return uint64(n), nil
}
