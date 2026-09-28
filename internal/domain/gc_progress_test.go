package domain

import (
	"errors"
	"testing"
)

// H3: GC progress is CAS-written operational metadata with a durable
// candidate cursor; failure reasons are a closed set; batch receipts derive
// their request IDs from the GC request.
func TestGCProgressAndFailureContract(t *testing.T) {
	p := GCProgress{SessionID: "s", GCRequestID: "gcq_x", Revision: 1}
	if err := p.Validate(); err != nil {
		t.Fatalf("fresh progress rejected: %v", err)
	}
	p.Cursor, p.Batches, p.Attempts, p.Revision = GCCursor{Seq: 9, ID: "itm_a"}, 1, 2, 2
	if err := p.Validate(); err != nil {
		t.Fatalf("advanced progress rejected: %v", err)
	}
	for name, bad := range map[string]GCProgress{
		"no revision":          {SessionID: "s", GCRequestID: "gcq_x"},
		"no request":           {SessionID: "s", Revision: 1},
		"cursor without batch": {SessionID: "s", GCRequestID: "gcq_x", Revision: 1, Cursor: GCCursor{Seq: 3, ID: "i"}},
		"zero seq with id":     {SessionID: "s", GCRequestID: "gcq_x", Revision: 1, Batches: 1, Cursor: GCCursor{ID: "i"}},
		"seq without id":       {SessionID: "s", GCRequestID: "gcq_x", Revision: 1, Batches: 1, Cursor: GCCursor{Seq: 3}},
	} {
		if !errors.Is(bad.Validate(), ErrInvalidRecord) {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, c := range []GCFailureCode{GCFailurePolicyMismatch, GCFailureInvalidRequest, GCFailureIntegrity, GCFailureAttemptsExhausted} {
		if !c.Valid() {
			t.Errorf("%s rejected", c)
		}
	}
	for _, c := range []GCFailureCode{"", "TIMEOUT", "integrity"} {
		if c.Valid() {
			t.Errorf("open failure code %q accepted", c)
		}
	}
	id, err := GCBatchRequestID("gcq_x", 3)
	if err != nil || id != "gcq_x/batch/3" {
		t.Fatalf("GCBatchRequestID = %q, %v", id, err)
	}
	if other, _ := GCBatchRequestID("gcq_x", 4); other == id {
		t.Fatal("batches share a request ID")
	}
	for _, bad := range []struct {
		req string
		n   uint64
	}{{"", 1}, {"gcq_x", 0}, {"a b", 1}} {
		if _, err := GCBatchRequestID(bad.req, bad.n); err == nil {
			t.Errorf("GCBatchRequestID(%q, %d) accepted", bad.req, bad.n)
		}
	}
}

// DUR-3.2: the session GC queue cursor is CAS-versioned operational state
// with a (Seq, ID) cursor, and trigger filters are canonical sets.
func TestGCQueueCursorAndTriggerSetContract(t *testing.T) {
	c := GCQueueCursor{SessionID: "s", Revision: 1}
	if err := c.Validate(); err != nil {
		t.Fatalf("fresh cursor rejected: %v", err)
	}
	c.Cursor, c.Revision = GCCursor{Seq: 4, ID: "gcq_a"}, 2
	if err := c.Validate(); err != nil {
		t.Fatalf("advanced cursor rejected: %v", err)
	}
	for name, bad := range map[string]GCQueueCursor{
		"no session":       {Revision: 1},
		"no revision":      {SessionID: "s"},
		"zero seq with id": {SessionID: "s", Revision: 1, Cursor: GCCursor{ID: "x"}},
		"seq without id":   {SessionID: "s", Revision: 1, Cursor: GCCursor{Seq: 2}},
	} {
		if !errors.Is(bad.Validate(), ErrInvalidRecord) {
			t.Errorf("%s: accepted", name)
		}
	}
	if !ValidGCTriggerSet(DefaultGCTriggers()) || !ValidGCTriggerSet([]GCTrigger{GCManual}) {
		t.Fatal("canonical trigger set rejected")
	}
	for _, bad := range [][]GCTrigger{nil, {}, {GCTTL, GCManual}, {GCManual, GCManual}, {"UNKNOWN"}} {
		if ValidGCTriggerSet(bad) {
			t.Errorf("trigger set %q accepted", bad)
		}
	}
}
