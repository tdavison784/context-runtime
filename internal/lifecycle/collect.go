package lifecycle

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

const methodCollect = "collect"

// Collect archives stale, superseded, duplicate, expired and ended-turn
// ephemeral candidates under gc/v1 (P3-38). The candidate set, decisions and
// results freeze in the committed receipt; a retry replays it and never scans
// again, even after later Unarchive. Only SYSTEM/HARNESS may collect, and each
// archived target needs its own Archive authority: entry authority is no
// ownership wildcard. Exceeding any work bound fails the whole collection.
func (s *Service) Collect(tx store.Tx, p domain.Principal, i domain.CollectIntent, seq uint64) (MutationOutcome, error) {
	return s.collect(tx, p, i, nil, seq)
}

// gcBatch links a collection to batch n of a durable GC request (H3): its
// record ID, and the stored progress (Revision 0 before the first batch)
// whose cursor the batch starts from.
type gcBatch struct {
	requestID string
	progress  domain.GCProgress
}

func (s *Service) collect(tx store.Tx, p domain.Principal, i domain.CollectIntent, link *gcBatch, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	sem, args, prior, err := s.begin(tx, p, domain.MutationCollection, methodCollect, i.RequestID, i)
	if err != nil {
		return out, err
	}
	gcRequestID := ""
	if link != nil {
		gcRequestID = link.requestID
	}
	if prior != nil {
		if prior.Result.Collect == nil || prior.Result.Collect.GCRequestID != gcRequestID {
			return out, domain.ErrIntegrity
		}
		return MutationOutcome{MutationReceiptID: prior.ID, Result: prior.Result.Clone()}, nil
	}
	// A new manual collection is a caller request: it can never name the
	// runtime GC namespaces its queued requests use (H5, SEC-2.6); a
	// committed receipt replayed above first (DUR-2.8).
	if link == nil {
		if err = domain.ValidateCallerRequestID(i.RequestID); err != nil {
			return out, err
		}
	}
	if err = i.Validate(); err != nil {
		return out, err
	}
	// seq 0 defers allocation until after the replay check above, so a
	// replay allocates nothing (DUR-1.3); an explicit seq must be allocated.
	if seq == 0 {
		seq = tx.NextSeq()
	} else if !tx.Allocated(seq) {
		return out, domain.ErrInvalidRecord
	}
	if p.Authority != domain.AuthoritySystem && p.Authority != domain.AuthorityHarness {
		return out, domain.ErrInvalidAuthorityPromotion
	}
	if !s.policy.GCTriggerEnabled(i.Trigger) {
		return out, ErrGCTriggerDisabled
	}
	if i.Scope == domain.CollectTask {
		if _, err = tx.Task(i.TaskID); errors.Is(err, domain.ErrNotFound) {
			return out, domain.ErrNotFound
		} else if err != nil {
			return out, err
		}
	}
	var progress domain.GCProgress
	if link != nil {
		progress = link.progress
	}
	plan, err := s.planBatch(tx, sem, p, i, seq, progress)
	if err != nil {
		return out, err
	}
	// A direct Collect is single-shot: exceeding one batch fails as a whole
	// (P3-39). Durable GC requests continue across batches instead (H3).
	if plan.more && link == nil {
		return out, domain.ErrResourceLimit
	}
	receipt, effects, spare := plan.receipt, plan.effects, plan.spare
	for _, e := range effects {
		if e.after, err = tx.UpdateItem(e.before.ID, e.before.Version, domain.ItemChange{Residency: ptr(domain.ResidencyArchived)}, e.audit); err != nil {
			return out, err
		}
		if err = recordEffect(tx, sem, e); err != nil {
			return out, err
		}
		receipt.ArchivedRefs = append(receipt.ArchivedRefs, domain.ItemRevisionRef{ItemID: e.after.ID, Version: e.after.Version})
	}
	receipt.GCRequestID = gcRequestID
	// An allocated sequence no archive consumed records the receipt itself.
	if spare == 0 {
		spare = tx.NextSeq()
	}
	receipt.SemanticMeta = domain.SemanticMeta{ID: collectReceiptID(p.SessionID, i.RequestID), SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: spare}
	if err = sem.InsertCollectReceipt(receipt); err != nil {
		return out, err
	}
	switch {
	case link != nil && plan.more:
		// More candidates remain: advance the durable cursor (CAS); the next
		// pass runs batch n+1 from it.
		next := domain.GCProgress{SessionID: p.SessionID, GCRequestID: link.requestID, Cursor: plan.next,
			Batches: link.progress.Batches + 1, Attempts: link.progress.Attempts, SnapshotSeq: receipt.SnapshotSeq, Revision: link.progress.Revision + 1}
		if _, err = sem.PutGCProgress(next, link.progress.Revision); err != nil {
			return out, err
		}
	case link != nil:
		result := domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID(p.SessionID, link.requestID), SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			GCRequestID: link.requestID, CollectReceiptID: receipt.ID, Outcome: domain.GCCollected}
		if err = sem.InsertGCResult(result); err != nil {
			return out, err
		}
	}
	out.Result.Collect = &receipt
	if err = s.finish(tx, sem, p, domain.MutationCollection, methodCollect, i.RequestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(tx, p, domain.MutationCollection, i.RequestID)
	return out, err
}

// batchPlan is one bounded batch of a collection: its frozen candidates and
// decisions, the archive effects to apply, a reserved sequence no archive
// consumed, and the durable cursor after its last candidate.
type batchPlan struct {
	receipt domain.CollectReceipt
	effects []itemEffect
	cursors []domain.GCCursor // position of each decided candidate
	spare   uint64
	next    domain.GCCursor
	more    bool
}

// planBatch freezes the candidates after cursor, in (Seq, ID) order, and
// their decisions before any effect (H3). It stops cleanly when the batch's
// work budget, MaxGCDecisions or MaxReceiptBytes would be exceeded and
// reports more, so a large collection proceeds across passes instead of
// failing as a whole. The caller's seq authorizes the first archive; a
// candidate whose authorization fails, or that the batch does not finish,
// returns its reserved seq for the next one. Candidates the collector may
// access but not archive, or whose own bounded read overflows, are recorded
// INELIGIBLE, never forced.
func (s *Service) planBatch(tx store.Tx, sem store.SemanticReader, p domain.Principal, i domain.CollectIntent, seq uint64, progress domain.GCProgress) (batchPlan, error) {
	after := progress.Cursor
	snap := seq - 1
	if progress.Batches > 0 {
		snap = progress.SnapshotSeq
	}
	plan := batchPlan{receipt: domain.CollectReceipt{RequestID: i.RequestID, PolicyVersion: s.policy.Version, Principal: p, SnapshotSeq: snap}, spare: seq, next: after}
	b := workBudget{remaining: s.policy.MaxTransactionWork, pageSize: s.policy.MaxPageSize}
	seqs := seqPool{spare: seq, next: tx.NextSeq}
	seen := map[string]bool{}
	cache := newGCCache()
	stop := func() (batchPlan, error) {
		plan.spare = seqs.spare
		if len(plan.receipt.Decisions) == 0 {
			return plan, domain.ErrResourceLimit // one candidate exceeds a whole batch
		}
		plan.more = true
		return s.fitReceipt(plan)
	}
	cursor := store.Cursor{Seq: after.Seq, ID: after.ID}
	for {
		room := s.policy.MaxGCDecisions - len(plan.receipt.Decisions)
		if room <= 0 {
			return stop()
		}
		if err := b.spend(1); err != nil {
			return stop()
		}
		page, err := sem.GCCandidates(store.GCCandidateFilter{Viewer: p, Scope: i.Scope, TaskID: i.TaskID, SnapshotSeq: snap, Page: store.Page{After: cursor, Limit: min(b.pageSize, room)}})
		if err != nil {
			return plan, err
		}
		if len(page.Records) > min(b.pageSize, room) {
			return plan, domain.ErrIntegrity
		}
		for _, it := range page.Records {
			if seen[it.ID] || it.SessionID != p.SessionID || it.Seq > snap || !it.Access.Permits(p) || i.Scope == domain.CollectTask && it.TaskID != i.TaskID ||
				it.Seq < cursor.Seq || it.Seq == cursor.Seq && it.ID <= cursor.ID {
				return plan, domain.ErrIntegrity
			}
			seen[it.ID] = true
			saved, spare := b, seqs.spare
			code, effect, err := s.decideCandidate(tx, sem, p, i, it, snap, cache, &b, &seqs)
			if errors.Is(err, errBudget) {
				// Unfinished candidate: nothing of it is kept; the next batch
				// starts with it.
				b, seqs.spare = saved, spare
				return stop()
			}
			if err != nil {
				return plan, err
			}
			ref := domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}
			if effect != nil {
				plan.effects = append(plan.effects, *effect)
			}
			plan.receipt.CandidateRefs = append(plan.receipt.CandidateRefs, ref)
			plan.receipt.Decisions = append(plan.receipt.Decisions, domain.GCDecision{Target: ref, Code: code})
			plan.next = domain.GCCursor{Seq: it.Seq, ID: it.ID}
			plan.cursors = append(plan.cursors, plan.next)
			cursor = store.Cursor{Seq: it.Seq, ID: it.ID}
		}
		if !page.More {
			plan.spare = seqs.spare
			return s.fitReceipt(plan)
		}
		if len(page.Records) == 0 {
			return plan, domain.ErrIntegrity
		}
	}
}

// decideCandidate decides one candidate. Cheap facts come first: only a
// possible archive pays for protection reads, so live items cannot exhaust
// the work bound (SEC-1.6). A candidate whose own bounded read overflows
// (not the batch budget) is INELIGIBLE; so is one the collector cannot
// archive (SEC-1.5, G2).
func (s *Service) decideCandidate(tx store.Tx, sem store.SemanticReader, p domain.Principal, i domain.CollectIntent, it domain.ContextItem, snap uint64, cache *gcCache, b *workBudget, seqs *seqPool) (domain.GCDecisionCode, *itemEffect, error) {
	gs, err := s.gcBase(tx, sem, it, snap, cache, b)
	if err != nil {
		return "", nil, err
	}
	code, _, err := policy.MayArchive(it, gs)
	if err != nil || code != domain.GCArchive {
		return code, nil, err
	}
	switch err := s.gcProtection(tx, sem, it, &gs, b); {
	case errors.Is(err, errBudget):
		return "", nil, err
	case errors.Is(err, domain.ErrResourceLimit), errors.Is(err, store.ErrLimitExceeded):
		return domain.GCIneligible, nil, nil
	case err != nil:
		return "", nil, err
	}
	if code, _, err = policy.CollectDecision(it, gs); err != nil || code != domain.GCArchive {
		return code, nil, err
	}
	if err := b.spend(3); err != nil {
		return "", nil, err
	}
	at := seqs.reserve()
	target := domain.ItemGrantTarget(p.SessionID, it.ID)
	auth, err := graph.AuthorizeAtSequence(tx, p, domain.ActionArchive, []domain.GrantTarget{target}, nil, at, s.policy.MaxTargets)
	switch {
	case errors.Is(err, domain.ErrInvalidAuthorityPromotion), errors.Is(err, store.ErrLimitExceeded):
		seqs.spare = at // unused: the next archive takes it
		return domain.GCIneligible, nil, nil
	case err != nil:
		return "", nil, err
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/collect-audit/v1").String(p.SessionID).String(i.RequestID).String(it.ID).Hash()
	return domain.GCArchive, &itemEffect{before: it, current: gs.Currentness, audit: domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: at,
		TargetKind: domain.TargetItem, TargetID: it.ID, Action: string(domain.ActionArchive), From: string(domain.ResidencyResident),
		To: string(domain.ResidencyArchived), Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}}, nil
}

// fitReceipt cuts a batch to the longest prefix whose frozen receipt fits
// MaxReceiptBytes, continuing from its last kept candidate.
func (s *Service) fitReceipt(plan batchPlan) (batchPlan, error) {
	for {
		if _, err := domain.CanonicalSemanticArguments(plan.receipt, s.policy.MaxReceiptBytes); err == nil {
			return plan, nil
		}
		n := len(plan.receipt.Decisions) / 2
		if n == 0 {
			return plan, domain.ErrResourceLimit
		}
		kept := map[string]bool{}
		for _, ref := range plan.receipt.CandidateRefs[:n] {
			kept[ref.ItemID] = true
		}
		var effects []itemEffect
		for _, e := range plan.effects {
			if kept[e.before.ID] {
				effects = append(effects, e)
			}
		}
		plan.receipt.CandidateRefs, plan.receipt.Decisions, plan.effects = plan.receipt.CandidateRefs[:n], plan.receipt.Decisions[:n], effects
		plan.cursors, plan.next, plan.more = plan.cursors[:n], plan.cursors[n-1], true
	}
}

// seqPool hands out the caller's seq first, then fresh allocations; a
// reserved seq nothing consumed returns as the next spare.
type seqPool struct {
	spare uint64
	next  func() uint64
}

func (p *seqPool) reserve() uint64 {
	if p.spare != 0 {
		v := p.spare
		p.spare = 0
		return v
	}
	return p.next()
}

func ptr[T any](v T) *T { return &v }

func collectReceiptID(session, request string) string {
	return "collect_" + domain.NewCanonicalEncoder("context-runtime/collect-receipt/v1").String(session).String(request).Hash()
}

func gcResultID(session, gcRequestID string) string {
	return "gcres_" + domain.NewCanonicalEncoder("context-runtime/gc-result/v1").String(session).String(gcRequestID).Hash()
}
