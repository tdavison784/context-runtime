// Package tools implements authenticated semantic handlers, without provider
// schemas or transport. Callers supply identities obtained from the harness.
package tools

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Error deliberately retains no underlying error or private diagnostic data.
type Error struct{ code domain.ToolErrorCode }

func (e *Error) Error() string              { return e.code.Message() }
func (e *Error) Code() domain.ToolErrorCode { return e.code }

// FixedError is also the boundary for errors returned by an outer Store.Update.
func FixedError(err error) error {
	if err == nil {
		return nil
	}
	code := domain.ToolErrorUnavailable
	var existing *Error
	switch {
	case errors.As(err, &existing):
		code = existing.code
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidAuthorityPromotion):
		code = domain.ToolErrorNotFound
	case errors.Is(err, domain.ErrEventIDConflict), errors.Is(err, domain.ErrVersionConflict):
		code = domain.ToolErrorConflict
	case errors.Is(err, domain.ErrInvalidRecord):
		code = domain.ToolErrorInvalidArgument
	case errors.Is(err, domain.ErrResourceLimit), errors.Is(err, store.ErrLimitExceeded):
		code = domain.ToolErrorTooLarge
	}
	return &Error{code: code}
}
