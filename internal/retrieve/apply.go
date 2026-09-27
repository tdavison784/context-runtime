package retrieve

import (
	"context"
	"errors"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Rehydrate admits historical data atomically for a trusted HARNESS origin
// only. Model context_get/context_rehydrate run Apply inside W5's execute
// transaction, which owns the tool receipt and result membership.
func (s *Service) Rehydrate(ctx context.Context, actor domain.Principal, intent AdmissionIntent, execution domain.Phase3Policy, allowStub bool) (domain.RetrievalResult, error) {
	if err := validateDenialOrigin(actor, intent); err != nil {
		_, fixed := FixedRetrievalError(err)
		return domain.RetrievalResult{}, fixed
	}
	if actor.Authority != domain.AuthorityHarness || intent.Origin.Invocation != nil {
		_, fixed := FixedRetrievalError(domain.ErrInvalidAuthorityPromotion)
		return domain.RetrievalResult{}, fixed
	}
	if err := execution.Validate(); err != nil {
		return domain.RetrievalResult{}, ErrRetrievalUnavailable
	}
	start := time.Now()
	var out domain.RetrievalResult
	err := s.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		var err error
		out, err = Apply(tx, actor, intent, execution, allowStub)
		return err
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.RetrievalResult{}, err
		}
		_, fixed := FixedRetrievalError(err)
		latency := uint64(time.Since(start).Nanoseconds())
		if auditErr := s.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
			return AppendDenial(tx, actor, intent, err, latency, execution)
		}); auditErr != nil {
			return domain.RetrievalResult{}, ErrRetrievalUnavailable
		}
		return domain.RetrievalResult{}, fixed
	}
	return out.Clone(), nil
}

// Apply lets a tool handler include retrieval and its tool execution receipt
// in one Store.Update. Every failure after allocation poisons that transaction.
func Apply(tx store.Tx, actor domain.Principal, intent AdmissionIntent, execution domain.Phase3Policy, allowStub bool) (out domain.RetrievalResult, err error) {
	if err = actor.Validate(); err != nil {
		return out, err
	}
	if err = execution.Validate(); err != nil {
		return out, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return out, err
	}
	if out, replayed, replayErr := replayRetrieval(sem, actor, intent, execution); replayErr != nil {
		return domain.RetrievalResult{}, replayErr
	} else if replayed {
		return out, nil
	}
	task, err := tx.Task(actor.TaskID)
	if err != nil {
		return out, err
	}
	conv, err := tx.Conversation(intent.Origin.ConversationID)
	if err != nil {
		return out, err
	}
	allowance, err := validateAdmission(actor, intent, task, conv, execution)
	if err != nil {
		return out, err
	}
	if err = validateToolOrigin(tx, sem, intent.Origin, execution.MaxPageSize, execution.MaxTransactionWork, true); err != nil {
		return out, err
	}
	got, err := readGet(tx, actor, intent.Rehydrate.ItemID)
	if err != nil {
		return out, err
	}
	var inherited *domain.ProjectionRecord
	var inheritedMembers []domain.CoverageMember
	if got.Item.Role == domain.RoleProjection {
		old, err := sem.ProjectionByItem(got.Item.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return out, domain.ErrIncompleteCoverage
		}
		if err != nil {
			return out, err
		}
		if err := CheckStoredProjectionDependencies(tx, sem, old, actor, task, conv, execution.MaxPageSize, execution.MaxTransactionWork); err != nil {
			return out, err
		}
		inherited = &old
		if inheritedMembers, err = coverageMembers(sem, old.DependencyCoverageID, execution.MaxPageSize, execution.MaxCoverageMembers); err != nil {
			return out, err
		}
	}
	source := got.Observed.Source
	lease, found, err := findActiveLease(sem, actor, source, task, conv, tx.LastSeq(), execution.MaxPageSize, execution.MaxTransactionWork)
	if err != nil {
		return out, err
	}
	args, err := retrievalArguments(intent, execution)
	if err != nil {
		return out, err
	}
	started := false
	defer func() {
		if started && err != nil {
			tx.Poison(err)
		}
	}()
	seqs := recordSeqs{}
	if !found {
		started = true
		seqs.Lease = tx.NextSeq()
	}
	started = true
	seqs.Coverage = tx.NextSeq()
	seqs.Item = tx.NextSeq()
	seqs.Projection = tx.NextSeq()
	seqs.Result = tx.NextSeq()
	seqs.Event = tx.NextSeq()
	seqs.Receipt = tx.NextSeq()
	input := recordInput{Source: got.Item, Observed: got.Observed, Task: task, Conversation: conv, Actor: actor,
		Intent: intent, Policy: execution, Arguments: args, Allowance: allowance, AllowStub: allowStub, Inherited: inherited, InheritedMembers: inheritedMembers, Seqs: seqs}
	if found {
		input.Existing = &lease
	}
	records, err := buildRetrievalRecords(input)
	if err != nil {
		return out, err
	}
	if records.NewLease {
		if err = sem.InsertRetrievalLease(records.Lease); err != nil {
			return out, err
		}
	}
	if err = sem.InsertCoverage(records.Coverage, records.Members); err != nil {
		return out, err
	}
	if err = tx.InsertItem(records.Item); err != nil {
		return out, err
	}
	if err = sem.InsertProjection(records.Projection); err != nil {
		return out, err
	}
	if err = sem.InsertRetrievalResult(records.Result); err != nil {
		return out, err
	}
	if err = sem.InsertRetrievalEvent(records.Event); err != nil {
		return out, err
	}
	if err = sem.InsertMutationReceipt(records.Receipt); err != nil {
		return out, err
	}
	// Never commit a projection its own dispatch checker would reject; the
	// caller receives a fixed error instead (SEC-1.14).
	if err = CheckStoredProjectionDependencies(tx, sem, records.Projection, actor, task, conv, execution.MaxPageSize, execution.MaxTransactionWork); err != nil {
		return out, err
	}
	return records.Result.Clone(), nil
}

// coverageMembers reads one coverage's complete member list within limit;
// a list that does not fit is a resource-limit rejection, never truncated.
func coverageMembers(r store.SemanticReader, id string, pageSize, limit int) ([]domain.CoverageMember, error) {
	if pageSize <= 0 || limit <= 0 {
		return nil, domain.ErrResourceLimit
	}
	var out []domain.CoverageMember
	after := store.Cursor{}
	for {
		n := min(pageSize, limit-len(out))
		if n <= 0 {
			return nil, domain.ErrResourceLimit
		}
		page, err := r.CoverageMembers(id, store.Page{After: after, Limit: n})
		if err != nil {
			return nil, err
		}
		if len(page.Records) > n || page.More && len(page.Records) == 0 {
			return nil, domain.ErrIntegrity
		}
		out = append(out, page.Records...)
		if !page.More {
			return out, nil
		}
		last := page.Records[len(page.Records)-1]
		if page.Next != (store.Cursor{Seq: last.Seq, ID: last.ID}) || page.Next.Seq < after.Seq || page.Next.Seq == after.Seq && page.Next.ID <= after.ID {
			return nil, domain.ErrIntegrity
		}
		after = page.Next
	}
}
