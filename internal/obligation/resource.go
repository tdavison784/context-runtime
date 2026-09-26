package obligation

import (
	"errors"
	"path"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// resultResourceBinding is the registration result kind (pending W1 request).
const resultResourceBinding = "RESOURCE_BINDING"

// RegisterResourceTx registers a resource and binds its reporter (P3-19,
// Q-7): the reporter is exactly the registering SYSTEM or HARNESS principal,
// and only it may later report. Registration establishes no baseline;
// currentness starts with the reporter's first resynchronization. Reporting
// authority grants no read access to obligations or proofs.
func (s *Service) RegisterResourceTx(tx store.Tx, actor domain.Principal, in domain.RegisterResourceIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationResourceRegister, in.RequestID, "RegisterResource", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
	b, err := s.registerResource(tx, sem, actor, in, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: resultResourceBinding, IDs: []string{b.ID}}}
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		tx.Poison(err)
		return domain.MutationResult{}, err
	}
	return result, nil
}

func (s *Service) registerResource(tx store.Tx, sem store.SemanticTx, actor domain.Principal, in domain.RegisterResourceIntent, seq uint64) (domain.ResourceBinding, error) {
	if !trustedControl(actor) || in.Reporter != actor {
		return domain.ResourceBinding{}, domain.ErrInvalidAuthorityPromotion
	}
	if !in.Access.Permits(actor) {
		return domain.ResourceBinding{}, domain.ErrInvalidRecord
	}
	if _, err := sem.ResourceBinding(in.ResourceID); err == nil {
		return domain.ResourceBinding{}, domain.ErrInvalidTransition
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.ResourceBinding{}, err
	}
	b := domain.ResourceBinding{
		SemanticMeta: domain.SemanticMeta{ID: recordID("rb_", "resource-binding", in.ResourceID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		ResourceID:   in.ResourceID,
		Owner:        actor,
		Reporter:     actor,
		Access:       in.Access,
	}
	if err := sem.InsertResourceBinding(b); err != nil {
		tx.Poison(err)
		return domain.ResourceBinding{}, err
	}
	return b, nil
}

// ReportResourceChangeTx records one authenticated, ordered resource report
// (P3-19, C-9) and atomically invalidates every current proof it may affect
// (P3-23). Only the resource's registered reporter reports, exactly as
// registered; reporting is independent of any task's lifecycle.
//
//   - A resynchronization sets an authoritative baseline or restores KNOWN.
//     It is the only way to establish or recover currentness; an observation
//     never does.
//   - An ordinary report must extend the current authoritative revision by
//     exactly one. A report that skips revisions, or arrives while the state
//     is UNKNOWN, commits UNKNOWN and invalidates every workspace and current
//     path dependency on the resource.
//   - A report at or behind the current revision is rejected and never rolls
//     the state back; its exact retry replays.
//
// The result names only the update, never affected targets or counts.
func (s *Service) ReportResourceChangeTx(tx store.Tx, actor domain.Principal, in domain.ReportResourceChangeIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	family := domain.MutationResourceReport
	if in.Resynchronization {
		family = domain.MutationResourceResync
	}
	req, err := s.newRequest(family, in.RequestID, "ReportResourceChange", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
	bind, err := sem.ResourceBinding(in.ResourceID)
	if err != nil || !bind.Access.Permits(actor) {
		return domain.MutationResult{}, notFound(err)
	}
	if bind.Reporter != actor {
		return domain.MutationResult{}, domain.ErrInvalidAuthorityPromotion
	}
	state, err := sem.ResourceState(in.ResourceID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.MutationResult{}, err
	}
	if in.ExpectedRevision != state.Revision {
		return domain.MutationResult{}, domain.ErrVersionConflict
	}
	prior := state.AuthoritativeRevision
	if state.Revision == 0 && !in.Resynchronization || in.ResultingAuthoritativeRevision <= prior || in.ExpectedAuthoritativeRevision < prior {
		return domain.MutationResult{}, domain.ErrInvalidTransition
	}

	u := domain.ResourceUpdate{
		SemanticMeta:                   domain.SemanticMeta{ID: recordID("ru_", "resource-update", in.ResourceID, in.RequestID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		ResourceID:                     in.ResourceID,
		RequestID:                      in.RequestID,
		Reporter:                       actor,
		ExpectedAuthoritativeRevision:  prior,
		ResultingAuthoritativeRevision: in.ResultingAuthoritativeRevision,
		Resynchronization:              in.Resynchronization,
	}
	var c change
	gap := in.ExpectedAuthoritativeRevision != prior || in.ResultingAuthoritativeRevision-prior != 1
	switch {
	case in.Resynchronization:
		u.Freshness, u.WorkspaceFingerprint, u.AllPaths = domain.ResourceKnown, in.WorkspaceFingerprint, true
		c = change{allPaths: true, fingerprint: in.WorkspaceFingerprint, contents: pathContents(in)}
	case gap || state.Freshness != domain.ResourceKnown:
		u.Freshness, u.AllPaths = domain.ResourceUnknown, true
		c = change{unknown: true, allPaths: true}
	default:
		u.Freshness, u.WorkspaceFingerprint, u.AllPaths, u.ChangedPaths = domain.ResourceKnown, in.WorkspaceFingerprint, in.AllPaths, in.Clone().ChangedPaths
		c = change{allPaths: in.AllPaths, paths: map[string]bool{}, fingerprint: in.WorkspaceFingerprint, contents: pathContents(in)}
		for _, p := range in.ChangedPaths {
			c.paths[p] = true
		}
	}
	w := &writes{tx: tx}
	w.start()
	if err := sem.InsertResourceUpdate(u); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	next := domain.ResourceState{
		SemanticMeta:          domain.SemanticMeta{ID: recordID("rs_", "resource-state", in.ResourceID), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		ResourceID:            in.ResourceID,
		BindingID:             bind.ID,
		LastUpdateID:          u.ID,
		AuthoritativeRevision: u.ResultingAuthoritativeRevision,
		WorkspaceFingerprint:  u.WorkspaceFingerprint,
		Freshness:             u.Freshness,
		Revision:              state.Revision + 1, // CAS result; the store assigns it
	}
	if _, err := sem.PutResourceState(next, state.Revision); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	if u.Freshness == domain.ResourceKnown {
		if err := s.recordPathContents(sem, u, in.PathContents); err != nil {
			return domain.MutationResult{}, w.fail(err)
		}
	}
	inv := invalidation{cause: domain.CauseResourceInvalidation, causeRecord: u.ID, requestID: in.RequestID, reason: domain.ReasonResourceChanged, rule: ResourceInvalidationRule}
	if err := s.invalidateResource(tx, sem, actor, seq, in.ResourceID, c, inv); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "RESOURCE_UPDATE", IDs: []string{u.ID}}}
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	return result, nil
}

func pathContents(in domain.ReportResourceChangeIntent) map[string]string {
	out := make(map[string]string, len(in.PathContents))
	for _, c := range in.PathContents {
		out[c.Path] = c.ContentHash
	}
	return out
}

// canonicalLocator is a locator's resource-relative form (base "."), the
// single identity under which authoritative path content is recorded, so a
// target bound as base "svc" + "a.go" and a report of "svc/a.go" agree.
func canonicalLocator(l domain.ResourceLocator) (domain.ResourceLocator, error) {
	return domain.ResourceLocatorV1(l.ResourceID, ".", path.Join(l.BaseDir, l.Path))
}

// recordPathContents stores the reported authoritative content of each path
// at the update's resulting revision (P3-19). Missing entries assert nothing.
func (s *Service) recordPathContents(sem store.SemanticTx, u domain.ResourceUpdate, contents []domain.ResourcePathContent) error {
	work := s.newBudget()
	for _, c := range contents {
		if err := work.spend(1); err != nil {
			return err
		}
		loc, err := domain.ResourceLocatorV1(u.ResourceID, ".", c.Path)
		if err != nil {
			return err
		}
		key, err := loc.Key()
		if err != nil {
			return err
		}
		var expected uint64
		if cur, err := sem.ResourcePathState(loc); err == nil {
			expected = cur.Revision
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		_, err = sem.PutResourcePathState(domain.ResourcePathState{
			SemanticMeta:     domain.SemanticMeta{ID: recordID("pst_", "path-state", key), SessionID: u.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: u.Seq},
			Locator:          loc,
			ContentHash:      c.ContentHash,
			ResourceUpdateID: u.ID,
			ResourceRevision: u.ResultingAuthoritativeRevision,
			Revision:         expected + 1, // CAS result; the store assigns it
			Freshness:        domain.ResourceKnown,
		}, expected)
		if err != nil {
			return err
		}
	}
	return nil
}

// currentPathState returns the path's recorded content if it still describes
// the resource's current KNOWN state: no later update was UNKNOWN, covered
// all paths, or listed the path (P3-19). Otherwise ok is false, which the
// file_read matcher treats as unknown.
func (s *Service) currentPathState(r store.SemanticReader, work *budget, loc domain.ResourceLocator, rs domain.ResourceState) (domain.ResourcePathState, bool, error) {
	loc, err := canonicalLocator(loc)
	if err != nil {
		return domain.ResourcePathState{}, false, nil
	}
	ps, err := r.ResourcePathState(loc)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ResourcePathState{}, false, nil
	}
	if err != nil {
		return domain.ResourcePathState{}, false, err
	}
	if rs.Freshness != domain.ResourceKnown || ps.Freshness != domain.ResourceKnown || ps.ResourceRevision > rs.AuthoritativeRevision {
		return domain.ResourcePathState{}, false, nil
	}
	full := path.Join(loc.BaseDir, loc.Path)
	stale := false
	err = s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
		pg, err := r.ResourceUpdates(loc.ResourceID, p)
		if err != nil {
			return 0, store.Cursor{}, false, err
		}
		for _, u := range pg.Records {
			if u.ResultingAuthoritativeRevision > ps.ResourceRevision &&
				(u.Freshness != domain.ResourceKnown || u.AllPaths || containsPath(u.ChangedPaths, full)) {
				stale = true
			}
		}
		return len(pg.Records), pg.Next, pg.More, nil
	})
	if err != nil || stale {
		return domain.ResourcePathState{}, false, err
	}
	return ps, true, nil
}

func containsPath(paths []string, p string) bool {
	for _, q := range paths {
		if q == p {
			return true
		}
	}
	return false
}
