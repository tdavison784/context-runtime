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

// G3 / SEC-1.2: a derived request ID is bound to the principal that owns its
// receipt. Another principal presenting it is refused before any receipt
// lookup, identically whether or not the owner's request exists, so it can
// neither probe hidden commands nor squat the owner's receipt.
func TestRuntimeRequestIDIsPrincipalBound(t *testing.T) {
	a := Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", Authority: AuthorityUser}
	b := a
	b.AgentID, b.Authority = "agent", AuthorityAgent
	occurrence := CallerOccurrenceID("s", "event")
	id, err := OperationRequestID(a, occurrence, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ReservedIDPrefix(id) || ValidateCallerRequestID(id) == nil {
		t.Fatalf("derived ID %q is outside the reserved runtime namespace", id)
	}
	owned, err := MutationReceiptID(a, MutationLifecycle, id)
	if err != nil {
		t.Fatalf("owner's derived request refused: %v", err)
	}
	if _, err := MutationReceiptID(b, MutationLifecycle, id); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("foreign principal accepted another principal's derived request: %v", err)
	}
	if other, _ := OperationRequestID(b, occurrence, 3, 1); other == id {
		t.Fatal("two principals derive the same request ID")
	}
	for _, change := range []func(*Principal){
		func(p *Principal) { p.WorkflowID = "other" },
		func(p *Principal) { p.TaskID = "other" },
		func(p *Principal) { p.Authority = AuthoritySystem },
	} {
		q := a
		change(&q)
		if _, err := MutationReceiptID(q, MutationLifecycle, id); err == nil {
			t.Fatalf("principal %+v accepted a request derived for %+v", q, a)
		}
	}
	for _, forged := range []string{"req_" + strings.Repeat("0", 32), id[:len(id)-1] + "0", strings.Replace(id, ".", "", 1), id + ".x", "req_x.y"} {
		if _, err := MutationReceiptID(a, MutationLifecycle, forged); err == nil {
			t.Errorf("forged runtime request %q accepted", forged)
		}
	}
	// Receipt identity values are unchanged by the binding: (session, family, request).
	c := a
	c.TaskID = "other"
	x, _ := MutationReceiptID(a, MutationLifecycle, "caller-request")
	y, _ := MutationReceiptID(c, MutationLifecycle, "caller-request")
	if x != y || owned == x {
		t.Fatal("receipt identity must remain (session, family, request)")
	}
	for _, change := range []func(*uint64, *uint64, *string){
		func(op, _ *uint64, _ *string) { *op = 4 },
		func(_, cmd *uint64, _ *string) { *cmd = 2 },
		func(_, _ *uint64, occ *string) { *occ = CallerOccurrenceID("s", "other") },
	} {
		op, cmd, occ := uint64(3), uint64(1), occurrence
		change(&op, &cmd, &occ)
		if other, _ := OperationRequestID(a, occ, op, cmd); other == id {
			t.Fatal("request ID omits its occurrence or ordinals")
		}
	}
}
