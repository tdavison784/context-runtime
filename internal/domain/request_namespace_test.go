package domain

import (
	"errors"
	"strings"
	"testing"
)

// G3 / SEC-1.2: runtime-derived request IDs live in a reserved namespace no
// caller may name, as an EventID or as a standalone intent RequestID.
func TestRuntimeRequestNamespaceIsReserved(t *testing.T) {
	for _, id := range []string{"req_", "req_0123456789abcdef", "req_x.y"} {
		if !ReservedIDPrefix(id) {
			t.Errorf("ReservedIDPrefix(%q) = false; req_ is a runtime namespace", id)
		}
		if err := (Event{EventID: id, Kind: EventUser}).validateShape(true); err == nil {
			t.Errorf("caller EventID %q accepted in the runtime request namespace", id)
		}
		if err := ValidateCallerRequestID(id); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("ValidateCallerRequestID(%q) = %v; want ErrInvalidRecord", id, err)
		}
	}
	for _, id := range []string{"request-1", "requests", "req-1", "REQ_1"} {
		if err := ValidateCallerRequestID(id); err != nil {
			t.Errorf("ValidateCallerRequestID(%q) = %v; ordinary caller IDs are allowed", id, err)
		}
	}
	for _, id := range []string{"", "a b", "itm_x", "evt_x", "evc_x"} {
		if ValidateCallerRequestID(id) == nil {
			t.Errorf("ValidateCallerRequestID(%q) accepted a malformed or reserved ID", id)
		}
	}
}

// allocated is a fake transaction: the sequences it allocated.
type allocated map[uint64]bool

func (a allocated) Allocated(seq uint64) bool { return a[seq] }

// G3 / SEC-1.2 / H5 / SEC-2.2: a runtime request ID binds the authenticated
// ingesting principal, the receipt owner, the occurrence and the event's
// own sequence. MutationReceiptID accepts it only for its owner and only in
// the transaction that allocated that sequence, before any receipt lookup:
// another principal, a caller predicting a future event, or anyone replaying
// a past one is refused identically, so there is neither oracle nor squat.
func TestRuntimeRequestIDIsOwnerAndTransactionBound(t *testing.T) {
	h := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", Authority: AuthorityHarness}
	a := h
	a.Authority = AuthorityUser // the lowered source actor
	b := a
	b.AgentID, b.Authority = "agent", AuthorityAgent
	occurrence := CallerOccurrenceID("s", "event")
	id, err := OperationRequestID(h, a, occurrence, 7, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ReservedIDPrefix(id) || ValidateCallerRequestID(id) == nil {
		t.Fatalf("derived ID %q is outside the reserved runtime namespace", id)
	}
	tx := allocated{7: true}
	owned, err := MutationReceiptID(tx, a, MutationLifecycle, id)
	if err != nil {
		t.Fatalf("owner's derived request refused in its own transaction: %v", err)
	}
	for name, probe := range map[string]struct {
		tx    SeqAllocator
		owner Principal
	}{
		"foreign principal":          {tx, b},
		"other transaction":          {allocated{8: true}, a},
		"no transaction":             {nil, a},
		"lowered actor, other tx":    {allocated{}, a},
		"authenticated as the owner": {tx, h},
	} {
		if _, err := MutationReceiptID(probe.tx, probe.owner, MutationLifecycle, id); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: runtime request accepted: %v", name, err)
		}
	}
	for name, other := range map[string]func() (string, error){
		"other authenticated principal": func() (string, error) {
			g := h
			g.AgentID = "relay-2"
			return OperationRequestID(g, a, occurrence, 7, 3, 1)
		},
		"other owner":      func() (string, error) { return OperationRequestID(h, b, occurrence, 7, 3, 1) },
		"other sequence":   func() (string, error) { return OperationRequestID(h, a, occurrence, 8, 3, 1) },
		"other occurrence": func() (string, error) { return OperationRequestID(h, a, CallerOccurrenceID("s", "e2"), 7, 3, 1) },
		"other operation":  func() (string, error) { return OperationRequestID(h, a, occurrence, 7, 4, 1) },
		"other command":    func() (string, error) { return OperationRequestID(h, a, occurrence, 7, 3, 2) },
	} {
		if got, err := other(); err != nil || got == id {
			t.Errorf("%s: derivation omits it (%v)", name, err)
		}
	}
	for _, forged := range []string{"req_" + strings.Repeat("0", 32), "req_7_" + strings.Repeat("0", 32) + ".x", id[:len(id)-1] + "0", strings.Replace(id, "req_7_", "req_8_", 1), "req_07_" + id[len("req_7_"):], "req_x.y"} {
		if _, err := MutationReceiptID(allocated{7: true, 8: true}, a, MutationLifecycle, forged); err == nil {
			t.Errorf("forged runtime request %q accepted", forged)
		}
	}
	if _, err := OperationRequestID(h, a, occurrence, 0, 3, 1); err == nil {
		t.Error("runtime request derived without an event sequence")
	}
	// Receipt identity values stay (session, family, request).
	key, _ := MutationReceiptKey("s", MutationLifecycle, id)
	x, _ := MutationReceiptID(nil, a, MutationLifecycle, "caller-request")
	y, _ := MutationReceiptKey("s", MutationLifecycle, "caller-request")
	if owned != key || x != y {
		t.Fatal("receipt identity must remain (session, family, request)")
	}
	if RuntimeRequestOwnedBy(a, id) != nil || RuntimeRequestOwnedBy(b, id) == nil || RuntimeRequestOwnedBy(b, "caller-request") != nil {
		t.Fatal("store ownership check disagrees with the derivation")
	}
}

// H5 / SEC-2.6: GC trigger request IDs bind the authenticated origin and,
// with GC request record IDs, live in reserved namespaces.
func TestGCRuntimeIDsAreReservedAndOriginBound(t *testing.T) {
	p := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", Authority: AuthorityUser}
	q := p
	q.TaskID = "other"
	id, err := GCTriggerRequestID(p, GCTaskCompletion, "t")
	if err != nil {
		t.Fatal(err)
	}
	if other, _ := GCTriggerRequestID(q, GCTaskCompletion, "t"); other == id {
		t.Fatal("GC trigger request omits its authenticated origin")
	}
	if other, _ := GCTriggerRequestID(p, GCSupersession, "t"); other == id {
		t.Fatal("GC trigger request omits its trigger")
	}
	record, err := GCRequestRecordID("s", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []string{id, record, "gc_x", "gcq_x", id + "/batch/1"} {
		if !ReservedIDPrefix(reserved) || ValidateCallerRequestID(reserved) == nil {
			t.Errorf("%q is callable by a caller", reserved)
		}
	}
}

// DUR-3.3 / SEC-4.4 / DUR-4.8: a re-arm identity derives from the failed
// request alone, in its own encoder domain: idempotent across actors and
// never equal to a runtime trigger or manual derivation over the same bytes.
func TestGCRearmRequestIDIsActorFreeAndDomainSeparated(t *testing.T) {
	p := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", Authority: AuthoritySystem}
	record, err := GCRequestRecordID("s", "gc_root")
	if err != nil {
		t.Fatal(err)
	}
	id, err := GCRearmRequestID(record)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := GCRearmRequestID(record); again != id {
		t.Fatal("re-arm identity is not deterministic")
	}
	if other, _ := GCRearmRequestID("gcq_other"); other == id {
		t.Fatal("re-arm identity ignores the failed request")
	}
	// Same bytes through the runtime trigger and (via a caller-named root)
	// manual derivations must not collide with the re-arm domain.
	trigger, _ := GCTriggerRequestID(p, GCTaskCompletion, record)
	if trigger == id {
		t.Fatal("re-arm identity aliases a runtime trigger derivation")
	}
	for _, reserved := range []string{id, "gc_" + id} {
		if !ReservedIDPrefix(reserved) || ValidateCallerRequestID(reserved) == nil {
			t.Errorf("%q is callable by a caller", reserved)
		}
	}
	if _, err := GCRearmRequestID(""); err == nil {
		t.Error("re-arm of an empty failed request accepted")
	}
}

// SEC-4.8: explicit (manual) collections derive in their own encoder domain.
// The same principal, trigger value and caller-named request a runtime
// producer hashes must not precompute the manual record (the wedge), and a
// manual request's batch continuations must not alias the runtime request's
// batch receipts.
func TestGCManualRequestIDIsDomainSeparated(t *testing.T) {
	p := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", Authority: AuthoritySystem}
	id, err := GCManualRequestID(p, "root-1")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := GCManualRequestID(p, "root-1"); again != id {
		t.Fatal("manual identity is not deterministic")
	}
	if other, _ := GCManualRequestID(p, "root-2"); other == id {
		t.Fatal("manual identity ignores the caller request")
	}
	mate := p
	mate.AgentID = "agent-2"
	if other, _ := GCManualRequestID(mate, "root-1"); other == id {
		t.Fatal("manual identity ignores the collector")
	}
	for _, trigger := range []GCTrigger{GCTaskCompletion, GCManual, GCSupersession} {
		if runtime, _ := GCTriggerRequestID(p, trigger, "root-1"); runtime == id {
			t.Fatalf("manual identity aliases a %s derivation", trigger)
		}
	}
	if rearm, _ := GCRearmRequestID(id); rearm == id {
		t.Fatal("manual identity aliases the re-arm domain")
	}
	// Batch continuations of a caller-named manual request re-root into the
	// manual domain, never the runtime derivation whose batches they would
	// otherwise collide with.
	manual := GCRequest{CollectIntent: CollectIntent{RequestID: "root-1", Scope: CollectTask, TaskID: "t", Trigger: GCTaskCompletion}, Origin: p}
	mine, err := manual.BatchRequestID(2)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := GCTriggerRequestID(p, GCTaskCompletion, "root-1")
	theirs, _ := GCBatchRequestID(runtime, 2)
	if mine == theirs {
		t.Fatal("manual batch continuation aliases a runtime batch receipt")
	}
	for _, reserved := range []string{id, mine} {
		if !ReservedIDPrefix(reserved) || ValidateCallerRequestID(reserved) == nil {
			t.Errorf("%q is callable by a caller", reserved)
		}
	}
	if _, err := GCManualRequestID(Principal{}, "root-1"); err == nil {
		t.Error("manual identity of an unauthenticated principal accepted")
	}
	if _, err := GCManualRequestID(p, ""); err == nil {
		t.Error("manual identity of an empty caller request accepted")
	}
}
