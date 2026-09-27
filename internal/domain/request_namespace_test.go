package domain

import "testing"

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
	}
}
