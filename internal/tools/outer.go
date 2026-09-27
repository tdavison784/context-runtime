package tools

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Execute is the outer boundary: it opens the one Store.Update for a handler
// and returns only closed public errors, never nested store diagnostics. The
// handler receives seq 0, so the operation sequence is allocated only after
// the replay check: an identical retry leaves LastSeq unchanged (FR-ING-006).
func Execute(ctx context.Context, st store.Store, session string, handler func(tx store.Tx, seq uint64) (domain.ToolResult, error)) (domain.ToolResult, error) {
	var result domain.ToolResult
	err := st.Update(ctx, session, func(tx store.Tx) error {
		var err error
		result, err = handler(tx, 0)
		return err
	})
	if err != nil {
		return domain.ToolResult{}, FixedError(err)
	}
	return result.Clone(), nil
}
