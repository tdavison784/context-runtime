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
	return s.collect(tx, p, i, "", seq)
}

func (s *Service) collect(tx store.Tx, p domain.Principal, i domain.CollectIntent, gcRequestID string, seq uint64) (out MutationOutcome, err error) {
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
	if prior != nil {
		if prior.Result.Collect == nil || prior.Result.Collect.GCRequestID != gcRequestID {
			return out, domain.ErrIntegrity
		}
		return MutationOutcome{MutationReceiptID: prior.ID, Result: prior.Result.Clone()}, nil
	}
	if err = i.Validate(); err != nil {
		return out, err
	}
	if !tx.Allocated(seq) || seq == 0 {
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
	receipt, effects, err := s.planCollection(tx, sem, p, i, seq)
	if err != nil {
		return out, err
	}
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
	receipt.SemanticMeta = domain.SemanticMeta{ID: collectReceiptID(p.SessionID, i.RequestID), SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}
	if err = sem.InsertCollectReceipt(receipt); err != nil {
		return out, err
	}
	if gcRequestID != "" {
		link := domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID(p.SessionID, gcRequestID), SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			GCRequestID: gcRequestID, CollectReceiptID: receipt.ID}
		if err = sem.InsertGCResult(link); err != nil {
			return out, err
		}
	}
	out.Result.Collect = &receipt
	if err = s.finish(tx, sem, p, domain.MutationCollection, methodCollect, i.RequestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(p, domain.MutationCollection, i.RequestID)
	return out, err
}

// planCollection freezes candidates and decisions before any effect. The
// caller's seq authorizes the first archive; a candidate whose authorization
// fails returns its reserved seq for the next one. Candidates the collector
// may access but not archive are recorded INELIGIBLE, never forced.
func (s *Service) planCollection(tx store.Tx, sem store.SemanticReader, p domain.Principal, i domain.CollectIntent, seq uint64) (domain.CollectReceipt, []itemEffect, error) {
	snap := seq - 1
	r := domain.CollectReceipt{RequestID: i.RequestID, PolicyVersion: s.policy.Version, Principal: p, SnapshotSeq: snap}
	b := workBudget{remaining: s.policy.MaxTransactionWork, pageSize: s.policy.MaxPageSize}
	candidates, err := drain(&b, s.policy.MaxGCDecisions, func(page store.Page) (store.ResultPage[domain.ContextItem], error) {
		return sem.GCCandidates(store.GCCandidateFilter{Viewer: p, Scope: i.Scope, TaskID: i.TaskID, SnapshotSeq: snap, Page: page})
	})
	if err != nil {
		return r, nil, err
	}
	var effects []itemEffect
	spare := seq
	reserve := func() uint64 {
		if spare != 0 {
			v := spare
			spare = 0
			return v
		}
		return tx.NextSeq()
	}
	seen := map[string]bool{}
	for _, it := range candidates {
		if seen[it.ID] || it.SessionID != p.SessionID || it.Seq > snap || !it.Access.Permits(p) || i.Scope == domain.CollectTask && it.TaskID != i.TaskID {
			return r, nil, domain.ErrIntegrity
		}
		seen[it.ID] = true
		gs, err := s.gcSnapshot(tx, sem, it, snap, &b)
		if err != nil {
			return r, nil, err
		}
		code, _, err := policy.CollectDecision(it, gs)
		if err != nil {
			return r, nil, err
		}
		ref := domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}
		if code == domain.GCArchive {
			at := reserve()
			target := domain.ItemGrantTarget(p.SessionID, it.ID)
			auth, err := graph.AuthorizeAtSequence(tx, p, domain.ActionArchive, []domain.GrantTarget{target}, nil, at, s.policy.MaxTargets)
			switch {
			case errors.Is(err, domain.ErrInvalidAuthorityPromotion):
				code, spare = domain.GCIneligible, at
			case err != nil:
				return r, nil, err
			default:
				if err := b.spend(3); err != nil {
					return r, nil, err
				}
				id := "life_" + domain.NewCanonicalEncoder("context-runtime/collect-audit/v1").String(p.SessionID).String(i.RequestID).String(it.ID).Hash()
				effects = append(effects, itemEffect{before: it, current: gs.Currentness, audit: domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: at,
					TargetKind: domain.TargetItem, TargetID: it.ID, Action: string(domain.ActionArchive), From: string(domain.ResidencyResident),
					To: string(domain.ResidencyArchived), Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}})
			}
		}
		r.CandidateRefs = append(r.CandidateRefs, ref)
		r.Decisions = append(r.Decisions, domain.GCDecision{Target: ref, Code: code})
	}
	return r, effects, nil
}

func ptr[T any](v T) *T { return &v }

func collectReceiptID(session, request string) string {
	return "collect_" + domain.NewCanonicalEncoder("context-runtime/collect-receipt/v1").String(session).String(request).Hash()
}

func gcResultID(session, gcRequestID string) string {
	return "gcres_" + domain.NewCanonicalEncoder("context-runtime/gc-result/v1").String(session).String(gcRequestID).Hash()
}
