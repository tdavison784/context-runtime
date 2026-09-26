package tools

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Execute is the outer boundary: it opens the one Store.Update for a handler
// and returns only closed public errors, never nested store diagnostics.
func Execute(ctx context.Context, st store.Store, i domain.ToolInvocation, handler func(store.Tx) (domain.ToolResult, error)) (domain.ToolResult, error) {
	var result domain.ToolResult
	err := st.Update(ctx, i.SessionID, func(tx store.Tx) error {
		var err error
		result, err = handler(tx)
		return err
	})
	if err != nil {
		return domain.ToolResult{}, FixedError(err)
	}
	return result.Clone(), nil
}
