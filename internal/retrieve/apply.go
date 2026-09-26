package retrieve

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Rehydrate admits historical data atomically. The authenticated actor and
// origin are supplied by the harness; model text cannot choose either one.
func (s *Service) Rehydrate(ctx context.Context, actor domain.Principal, intent AdmissionIntent, execution domain.Phase3Policy, allowStub bool) (domain.RetrievalResult, error) {
	if err := actor.Validate(); err != nil {
		return domain.RetrievalResult{}, err
	}
	var out domain.RetrievalResult
	err := s.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		var err error
		out, err = Apply(tx, actor, intent, execution, allowStub)
		return err
	})
	if err != nil {
		return domain.RetrievalResult{}, err
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
	if err = validateToolOrigin(sem, intent.Origin, execution.MaxPageSize, execution.MaxTransactionWork); err != nil {
		return out, err
	}
	got, err := readGet(tx, actor, intent.Rehydrate.ItemID)
	if err != nil {
		return out, err
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
		Intent: intent, Policy: execution, Arguments: args, Allowance: allowance, AllowStub: allowStub, Seqs: seqs}
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
	if err = sem.InsertCoverage(records.Coverage, []domain.CoverageMember{records.Member}); err != nil {
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
	return records.Result.Clone(), nil
}
