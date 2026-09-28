package domain

import (
	"bytes"
	"testing"
)

func TestSemanticCanonicalSetsPresenceAndBounds(t *testing.T) {
	type args struct {
		Set     []string `canonical:"set"`
		Ordered []string
		Option  *string
	}
	a := args{Set: []string{"b", "a"}}
	b := args{Set: []string{"a", "b"}}
	x, err := CanonicalSemanticArguments(a, 4096)
	if err != nil {
		t.Fatal(err)
	}
	y, _ := CanonicalSemanticArguments(b, 4096)
	if !bytes.Equal(x, y) {
		t.Fatal("semantic sets ordered")
	}
	b.Ordered = []string{}
	y, _ = CanonicalSemanticArguments(b, 4096)
	if bytes.Equal(x, y) {
		t.Fatal("nil slice presence lost")
	}
	a.Set = []string{"a", "a"}
	if _, err := CanonicalSemanticArguments(a, 4096); err == nil {
		t.Fatal("duplicate set accepted")
	}
	if _, err := CanonicalSemanticArguments(b, 8); err == nil {
		t.Fatal("bound ignored")
	}
}
