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
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
	b, err := s.registerResource(tx, sem, actor, in, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: resultResourceBinding, IDs: []string{b.ID}}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
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
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
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
	gap := in.ExpectedAuthoritativeRevision != prior || in.ResultingAuthoritativeRevision-prior != 1
	switch {
	case in.Resynchronization:
		u.Freshness, u.WorkspaceFingerprint, u.AllPaths = domain.ResourceKnown, in.WorkspaceFingerprint, true
	case gap || state.Freshness != domain.ResourceKnown:
		u.Freshness, u.AllPaths = domain.ResourceUnknown, true
	default:
		u.Freshness, u.WorkspaceFingerprint, u.AllPaths, u.ChangedPaths = domain.ResourceKnown, in.WorkspaceFingerprint, in.AllPaths, in.Clone().ChangedPaths
	}
	work := s.newBudget() // one budget for the whole report (DUR-1.12)
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
		if err := s.recordPathContents(sem, work, u, in.PathContents); err != nil {
			return domain.MutationResult{}, w.fail(err)
		}
	}
	// K1a: the report never fans out. Every dependent proof's validity is
	// derived at read from the store's write-time pointers, which this
	// report's own writes maintain; settlement is recorded inline before the
	// next transition on a version or by the asynchronous worker.
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "RESOURCE_UPDATE", IDs: []string{u.ID}}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	return result, nil
}

// canonicalLocator is a locator's resource-relative form (base "."), the
// single identity under which authoritative path content is recorded, so a
// target bound as base "svc" + "a.go" and a report of "svc/a.go" agree.
func canonicalLocator(l domain.ResourceLocator) (domain.ResourceLocator, error) {
	return domain.ResourceLocatorV1(l.ResourceID, ".", path.Join(l.BaseDir, l.Path))
}

// recordPathContents stores the reported authoritative content of each path
// at the update's resulting revision (P3-19). Missing entries assert nothing.
func (s *Service) recordPathContents(sem store.SemanticTx, work *budget, u domain.ResourceUpdate, contents []domain.ResourcePathContent) error {
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

// currentPathState is the shared path-currency rule
// (store.CurrentPathContent), charged to the transaction's work budget: the
// path's recorded content while it still describes the resource's current
// KNOWN state. ok is false otherwise, which the file_read matcher and
// CURRENT_PATH claims treat as unknown.
func (s *Service) currentPathState(r store.SemanticReader, work *budget, loc domain.ResourceLocator, rs domain.ResourceState) (domain.ResourcePathState, bool, error) {
	if err := work.spend(1); err != nil {
		return domain.ResourcePathState{}, false, err
	}
	return store.CurrentPathContent(r, loc, rs)
}
