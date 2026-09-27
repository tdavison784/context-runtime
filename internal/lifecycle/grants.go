package lifecycle

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Grant receipt methods. The issued/revoked grant is the frozen result's
// record reference; MutationOutcome.GrantID stays empty because issuance and
// revocation require direct authority and are never delegated (P3-11).
const (
	methodIssueGrant  = "issue_grant"
	methodRevokeGrant = "revoke_grant"
)

// IssueGrant derives issuer, session and issued sequence from the
// authenticated actor and the allocated seq; callers cannot certify them.
// Every target must already exist in this transaction, be accessible to the
// issuer and be within its direct authority, so no delegation chain exists.
func (s *Service) IssueGrant(tx store.Tx, p domain.Principal, i domain.GrantIntent, seq uint64) (MutationOutcome, error) {
	return s.executeRecord(tx, p, methodIssueGrant, i.RequestID, i, seq, func(sem store.SemanticTx) (domain.RecordResult, error) {
		return s.issueGrant(tx, sem, p, i, seq)
	})
}

// RevokeGrant resolves the grant's original exact targets, including
// retired obligation versions, and requires direct authority over each.
func (s *Service) RevokeGrant(tx store.Tx, p domain.Principal, i domain.RevokeGrantIntent, seq uint64) (MutationOutcome, error) {
	return s.executeRecord(tx, p, methodRevokeGrant, i.RequestID, i, seq, func(sem store.SemanticTx) (domain.RecordResult, error) {
		return s.revokeGrant(tx, sem, p, i, seq)
	})
}

func (s *Service) IssueGrantStandalone(ctx context.Context, p domain.Principal, i domain.GrantIntent) (domain.RecordResult, error) {
	return s.standaloneRecord(ctx, p, methodIssueGrant, i.RequestID, i, func(tx store.Tx, seq uint64) (MutationOutcome, error) {
		return s.IssueGrant(tx, p, i, seq)
	})
}

func (s *Service) RevokeGrantStandalone(ctx context.Context, p domain.Principal, i domain.RevokeGrantIntent) (domain.RecordResult, error) {
	return s.standaloneRecord(ctx, p, methodRevokeGrant, i.RequestID, i, func(tx store.Tx, seq uint64) (MutationOutcome, error) {
		return s.RevokeGrant(tx, p, i, seq)
	})
}

func (s *Service) executeRecord(tx store.Tx, p domain.Principal, method, requestID string, intent any, seq uint64, apply func(store.SemanticTx) (domain.RecordResult, error)) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	sem, args, prior, err := s.begin(tx, p, domain.MutationGrantFamily, method, requestID, intent)
	if err != nil {
		return out, err
	}
	if prior != nil {
		if prior.Result.Records == nil {
			return out, domain.ErrIntegrity
		}
		return MutationOutcome{MutationReceiptID: prior.ID, Result: prior.Result.Clone()}, nil
	}
	if !tx.Allocated(seq) || seq == 0 {
		return out, domain.ErrInvalidRecord
	}
	rec, err := apply(sem)
	if err != nil {
		return out, err
	}
	out.Result.Records = &rec
	if err = s.finish(tx, sem, p, domain.MutationGrantFamily, method, requestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(p, domain.MutationGrantFamily, requestID)
	return out, err
}

func (s *Service) standaloneRecord(ctx context.Context, p domain.Principal, method, requestID string, intent any, run func(store.Tx, uint64) (MutationOutcome, error)) (domain.RecordResult, error) {
	var out MutationOutcome
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		_, _, prior, err := s.begin(tx, p, domain.MutationGrantFamily, method, requestID, intent)
		if err != nil {
			return err
		}
		var seq uint64
		if prior == nil {
			seq = tx.NextSeq()
		}
		out, err = run(tx, seq)
		return err
	})
	if err != nil {
		return domain.RecordResult{}, err
	}
	return out.Result.Records.Clone(), nil
}

func (s *Service) issueGrant(tx store.Tx, sem store.SemanticReader, p domain.Principal, i domain.GrantIntent, seq uint64) (domain.RecordResult, error) {
	if err := i.Validate(); err != nil {
		return domain.RecordResult{}, err
	}
	if len(i.Targets) > s.policy.MaxTargets {
		return domain.RecordResult{}, domain.ErrResourceLimit
	}
	if !p.Authority.CanHoldLifecycleAuthority() {
		return domain.RecordResult{}, domain.ErrInvalidAuthorityPromotion
	}
	if i.ExpiresAtSeq != 0 && i.ExpiresAtSeq < seq {
		return domain.RecordResult{}, domain.ErrInvalidRecord
	}
	targets, err := grantTargets(tx, sem, p, i.Targets, nil)
	if err != nil {
		return domain.RecordResult{}, err
	}
	if err := s.liveGrantRoom(sem, i.Action, i.Targets, seq); err != nil {
		return domain.RecordResult{}, err
	}
	g := i.Clone()
	grant := domain.MutationGrant{ID: g.GrantID, SessionID: p.SessionID, Action: g.Action, Targets: g.Targets, Issuer: p,
		Grantee: g.Grantee, Matcher: g.Matcher, IssuedSeq: seq, ExpiresAtSeq: g.ExpiresAtSeq}
	if err := domain.AuthorizeGrantIssuance(grant, targets); err != nil {
		return domain.RecordResult{}, err
	}
	if err := tx.InsertGrant(grant); err != nil {
		return domain.RecordResult{}, err
	}
	return domain.RecordResult{Kind: "GRANT", IDs: []string{grant.ID}}, nil
}

func (s *Service) revokeGrant(tx store.Tx, sem store.SemanticReader, p domain.Principal, i domain.RevokeGrantIntent, seq uint64) (domain.RecordResult, error) {
	if err := i.Validate(); err != nil {
		return domain.RecordResult{}, err
	}
	g, err := tx.Grant(i.GrantID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.RecordResult{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.RecordResult{}, err
	}
	if g.SessionID != p.SessionID {
		return domain.RecordResult{}, domain.ErrNotFound
	}
	if len(g.Targets)+len(g.TargetIDs) > s.policy.MaxTargets {
		return domain.RecordResult{}, domain.ErrResourceLimit
	}
	targets, err := grantTargets(tx, sem, p, g.Targets, g.TargetIDs)
	if err != nil {
		return domain.RecordResult{}, err
	}
	if err := domain.AuthorizeGrantRevocation(p, g, targets); err != nil {
		return domain.RecordResult{}, err
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/grant-revocation-audit/v1").String(p.SessionID).String(i.RequestID).String(g.ID).Hash()
	ev := domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetGrant, TargetID: g.ID,
		Action: methodRevokeGrant, From: "ACTIVE", To: "REVOKED", Actor: p}
	if _, err := tx.RevokeGrant(g.ID, ev); err != nil {
		return domain.RecordResult{}, err
	}
	return domain.RecordResult{Kind: "GRANT", IDs: []string{g.ID}}, nil
}

// liveGrantRoom keeps producers within their consumer's bound (G2):
// authorization reads at most MaxTargets grants per (action, target), so
// issuance refuses a grant that would exceed that many live at seq. Grants
// revoked or expired by seq do not count; excluding them from the read
// itself is the store's live-only index.
func (s *Service) liveGrantRoom(sem store.SemanticReader, action domain.Action, targets []domain.GrantTarget, seq uint64) error {
	for _, t := range targets {
		found, err := sem.GrantsFor(action, t, s.policy.MaxTargets)
		if errors.Is(err, store.ErrLimitExceeded) {
			return domain.ErrResourceLimit
		}
		if err != nil {
			return err
		}
		live := 0
		for _, g := range found {
			if (g.RevokedSeq == 0 || seq < g.RevokedSeq) && (g.ExpiresAtSeq == 0 || seq <= g.ExpiresAtSeq) {
				live++
			}
		}
		if live >= s.policy.MaxTargets {
			return domain.ErrResourceLimit
		}
	}
	return nil
}

// grantTargets resolves exact stored targets. Missing, cross-session and
// inaccessible targets are indistinguishable (ErrNotFound). Obligation
// targets resolve their exact version, so retired versions stay revocable.
func grantTargets(tx store.Tx, sem store.SemanticReader, p domain.Principal, refs []domain.GrantTarget, legacy []string) ([]domain.MutationTarget, error) {
	var out []domain.MutationTarget
	item := func(id string) (domain.ContextItem, error) {
		it, err := tx.Item(id)
		if errors.Is(err, domain.ErrNotFound) || err == nil && !it.Access.Permits(p) {
			return domain.ContextItem{}, domain.ErrNotFound
		}
		return it, err
	}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		if ref.SessionID != p.SessionID {
			return nil, domain.ErrNotFound
		}
		t := domain.MutationTarget{Ref: ref}
		switch ref.Kind {
		case domain.GrantTargetItem:
			it, err := item(ref.ItemID)
			if err != nil {
				return nil, err
			}
			t.ID, t.Authority, t.Access = it.ID, it.Authority, it.Access
		case domain.GrantTargetObligation:
			o, err := sem.ExactObligation(domain.ObligationRef{SessionID: ref.SessionID, ObligationID: ref.ObligationID, Version: ref.Version})
			if errors.Is(err, domain.ErrNotFound) || err == nil && !o.Access.Permits(p) {
				return nil, domain.ErrNotFound
			}
			if err != nil {
				return nil, err
			}
			t.ID, t.Authority, t.Access = o.ObligationID, o.SourceAuthority, o.Access
		default:
			return nil, domain.ErrInvalidRecord
		}
		out = append(out, t)
	}
	for _, id := range legacy {
		it, err := item(id)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MutationTarget{ID: it.ID, Authority: it.Authority, Access: it.Access})
	}
	return out, nil
}
