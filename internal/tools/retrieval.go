package tools

import (
	"context"
	"errors"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/retrieve"
	"github.com/tdavison784/context-runtime/internal/store"
)

const (
	MethodGet       = "context_get"
	MethodRehydrate = "context_rehydrate"
)

// Get is model retrieval: admission with a holder-bound lease, unlike the
// harness's read-only historical Get (P3-28).
func (s *Service) Get(tx store.Tx, dispatcher domain.Principal, r Request[domain.RehydrateIntent], seq uint64) (domain.ToolResult, error) {
	return s.retrieval(tx, dispatcher, r, seq, MethodGet)
}

// Rehydrate is the model's explicit rehydration of historical data (P3-28/29).
func (s *Service) Rehydrate(tx store.Tx, dispatcher domain.Principal, r Request[domain.RehydrateIntent], seq uint64) (domain.ToolResult, error) {
	return s.retrieval(tx, dispatcher, r, seq, MethodRehydrate)
}

// retrieval runs W6's retrieve.Apply inside the one execute transaction. The
// lease, projection, result, event and retrieval receipt commit together with
// the projection's TOOL_RESULT membership and the tool receipts; membership
// is registered only after Apply succeeds.
func (s *Service) retrieval(tx store.Tx, dispatcher domain.Principal, r Request[domain.RehydrateIntent], seq uint64, method string) (domain.ToolResult, error) {
	i := r.Invocation
	return executeSourced(s, tx, dispatcher, r, method, r.Intent.RequestID, seq, func(tx store.Tx, sem store.SemanticTx, _ invocationState) (domain.ToolResult, *domain.ItemContentRef, error) {
		out, err := retrieve.Apply(tx, i.Principal, admission(i, r.Intent, method), s.policy, false)
		if err != nil {
			return domain.ToolResult{}, nil, err
		}
		projection, err := sem.Projection(out.ProjectionID)
		if err != nil {
			return domain.ToolResult{}, nil, err
		}
		item, err := tx.Item(projection.ItemID)
		if err != nil {
			return domain.ToolResult{}, nil, err
		}
		if projection.RetrievalResultID != out.ID || item.Role != domain.RoleProjection {
			return domain.ToolResult{}, nil, domain.ErrIntegrity
		}
		return domain.ToolResult{RetrievalResultID: out.ID}, &domain.ItemContentRef{ItemID: item.ID, ContentHash: item.ContentHash}, nil
	})
}

func admission(i domain.ToolInvocation, intent domain.RehydrateIntent, method string) retrieve.AdmissionIntent {
	inv := i
	return retrieve.AdmissionIntent{Rehydrate: intent, Method: method,
		Origin: domain.RetrievalOrigin{Holder: i.Principal, ConversationID: i.ConversationID, TurnID: i.TurnID, Invocation: &inv}}
}

// RunRetrieval is the outer boundary for model retrieval. A failed request
// rolls back, then W6's denial audit is recorded in its own transaction; the
// caller sees only W6's closed retrieval error code (P3-30). An untrusted
// dispatcher is refused before any read or audit write.
func (s *Service) RunRetrieval(ctx context.Context, st store.Store, dispatcher domain.Principal, r Request[domain.RehydrateIntent], method string) (domain.ToolResult, error) {
	i := r.Invocation
	if method != MethodGet && method != MethodRehydrate {
		return domain.ToolResult{}, &Error{code: domain.ToolErrorInvalidArgument}
	}
	if i.Validate() != nil || dispatcher.SessionID != i.SessionID || dispatcher.Validate() != nil ||
		dispatcher.Authority != domain.AuthorityHarness && dispatcher.Authority != domain.AuthoritySystem ||
		dispatcher.WorkflowID != i.Principal.WorkflowID || dispatcher.TaskID != i.Principal.TaskID || dispatcher.AgentID != i.Principal.AgentID {
		return domain.ToolResult{}, &Error{code: domain.ToolErrorNotFound}
	}
	start := time.Now()
	var result domain.ToolResult
	err := st.Update(ctx, i.SessionID, func(tx store.Tx) error {
		var err error
		result, err = s.retrieval(tx, dispatcher, r, 0, method)
		return err
	})
	if err == nil {
		return result.Clone(), nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ToolResult{}, err
	}
	code, _ := retrieve.FixedRetrievalError(err)
	latency := uint64(time.Since(start).Nanoseconds())
	if audit := st.Update(ctx, i.SessionID, func(tx store.Tx) error {
		return retrieve.AppendDenial(tx, i.Principal, admission(i, r.Intent, method), err, latency, s.policy)
	}); audit != nil {
		code = domain.ToolErrorUnavailable
	}
	return domain.ToolResult{}, &Error{code: code}
}
