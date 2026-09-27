package tools

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestFixedErrorsNeverExposeNestedDetails(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  domain.ToolErrorCode
	}{
		{domain.ErrNotFound, domain.ToolErrorNotFound},
		{domain.ErrInvalidAuthorityPromotion, domain.ToolErrorNotFound},
		{domain.ErrEventIDConflict, domain.ToolErrorConflict},
		{domain.ErrVersionConflict, domain.ToolErrorConflict},
		{domain.ErrInvalidRecord, domain.ToolErrorInvalidArgument},
		{domain.ErrResourceLimit, domain.ToolErrorTooLarge},
		{store.ErrLimitExceeded, domain.ToolErrorTooLarge},
		{domain.ErrIncompleteCoverage, domain.ToolErrorUnavailable},
		{domain.ErrIntegrity, domain.ToolErrorUnavailable},
		{errors.New("private source 17: secret content"), domain.ToolErrorUnavailable},
	} {
		err := FixedError(fmt.Errorf("private item secret, citation 7 of 9: %w", tc.cause))
		var toolError *Error
		if !errors.As(err, &toolError) || toolError.Code() != tc.code || err.Error() != tc.code.Message() {
			t.Fatalf("unexpected public error: %v", err)
		}
		if errors.Unwrap(err) != nil {
			t.Fatal("public error retains nested details")
		}
		if again := FixedError(err); again.Error() != err.Error() {
			t.Fatal("sanitizing twice changes error")
		}
	}
	if FixedError(nil) != nil {
		t.Fatal("nil became an error")
	}
}
