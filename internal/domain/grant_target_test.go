package domain

import "testing"

func TestGrantTargetCanonicalIsolation(t *testing.T) {
	item := ItemGrantTarget("s", "same")
	v1 := ObligationGrantTarget("s", "same", 1)
	v2 := ObligationGrantTarget("s", "same", 2)
	seen := map[string]bool{}
	for _, target := range []GrantTarget{item, v1, v2, ItemGrantTarget("other", "same")} {
		if err := target.Validate(); err != nil {
			t.Fatal(err)
		}
		if seen[target.AuthorizationKey] {
			t.Fatal("typed target collision")
		}
		seen[target.AuthorizationKey] = true
	}
	v2.AuthorizationKey = v1.AuthorizationKey
	if v2.Validate() == nil {
		t.Fatal("accepted forged key")
	}
	v1.Version = 0
	if v1.Validate() == nil {
		t.Fatal("accepted latest alias")
	}
}
