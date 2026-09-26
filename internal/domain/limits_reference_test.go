package domain

import (
	"errors"
	"testing"
)

// TestMaxReferenceLinks checks the References fan-out limit (D17, D14):
// default 256, zero selects the default, and a negative value is invalid.
func TestMaxReferenceLinks(t *testing.T) {
	if got := DefaultLimits().MaxReferenceLinks; got != 256 {
		t.Errorf("default MaxReferenceLinks = %d, want 256", got)
	}
	if got := (Limits{}).Effective().MaxReferenceLinks; got != 256 {
		t.Errorf("zero MaxReferenceLinks resolves to %d, want the default 256", got)
	}
	if err := (Limits{MaxReferenceLinks: -1}).Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Errorf("negative MaxReferenceLinks: error = %v, want ErrInvalidRecord", err)
	}
	if err := (Limits{MaxReferenceLinks: 1}).Validate(); err != nil {
		t.Errorf("MaxReferenceLinks 1: %v", err)
	}
}
