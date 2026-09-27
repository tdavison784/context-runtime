package domain

import (
	"errors"
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
