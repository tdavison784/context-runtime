package domain

import (
	"reflect"
	"testing"
)

func TestEverySubmittedOperationRejectsCallerRequestIdentity(t *testing.T) {
	typ := reflect.TypeOf(SemanticOperation{})
	for n := 0; n < typ.NumField(); n++ {
		field := typ.Field(n)
		kind := SemanticOperationKind(field.Tag.Get("operation"))
		if kind == "" || kind == OperationSpan {
			continue
		}
		op := SemanticOperation{Kind: kind, References: []OperationReference{{Slot: OperationSourceItem, Alias: "earlier"}}}
		payload := reflect.New(field.Type.Elem())
		reflect.ValueOf(&op).Elem().Field(n).Set(payload)
		request := payload.Elem().FieldByName("RequestID")
		if !request.IsValid() {
			t.Fatalf("%s has no intent identity", kind)
		}
		if err := op.Validate(); err != nil {
			t.Fatalf("%s empty request before alias resolution: %v", kind, err)
		}
		request.SetString("attacker-selected-receipt")
		if op.Validate() == nil {
			t.Errorf("%s accepted caller identity through an unresolved alias", kind)
		}
	}
}

func TestSubmittedAndResolvedOperationIdentityAreDistinct(t *testing.T) {
	op := SemanticOperation{Kind: OperationRevokeGrant, RevokeGrant: &RevokeGrantIntent{GrantID: "grant"}}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	if op.RevokeGrant.RequestID != "" {
		t.Fatal("validation mutated submitted request")
	}
	if op.ValidateResolved() == nil {
		t.Fatal("service accepted missing derived request identity")
	}
	derived, err := OperationRequestID(Principal{SessionID: "s", Authority: AuthorityUser}, Principal{SessionID: "s", Authority: AuthorityUser}, CallerOccurrenceID("s", "event"), 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	op.RevokeGrant.RequestID = derived
	if err := op.ValidateResolved(); err != nil {
		t.Fatal(err)
	}
	if op.Validate() == nil {
		t.Fatal("derived request accidentally accepted as caller payload")
	}
}
