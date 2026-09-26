package domain

import "testing"

func TestSemanticCanonicalGolden(t *testing.T) {
	locator, _ := (ResourceLocator{ResourceID: "repo", BaseDir: "src", Path: "a.go"}).Key()
	subject, _ := (ObservationSubject{Family: ObservationFileRead, Target: TargetSpec{File: &FileTarget{Locator: ResourceLocator{ResourceID: "repo", BaseDir: "src", Path: "a.go"}, Mode: FileCurrentContent}}}).Key()
	p := Principal{SessionID: "s", Authority: AuthoritySystem}
	e := Event{Kind: EventSystem, Spans: []Span{{Authority: AuthoritySystem, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, Parts: []InputPart{{Type: PartText, Text: "hello"}}}}}
	v2, _ := e.PayloadHashFor(RequestHashV2, p, Limits{}, Phase3Policy{})
	v3, err := e.PayloadHashFor(RequestHashV3, p, Limits{}, semanticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	args, err := CanonicalSemanticArguments(ResolveIntent{RequestID: "r", ItemID: "i", ExpectedVersion: 1}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, got, want string }{
		{"item-target", ItemGrantTarget("s", "same").AuthorizationKey, "sha256:b8bd2f2de3ffb93f30c919efc7ed14fb84e7f8d83d52b1f2fa28e12d9db07d25"},
		{"obligation-target", ObligationGrantTarget("s", "same", 2).AuthorizationKey, "sha256:22223e10928b07b6813b788cfb0d8dab39725b3823cf4f5e30c87a010d20683c"},
		{"locator", locator, "sha256:93ea24e1f92171d9bee0173a9c1bdd05863d19b4fd83862fb81c207cb93e253f"},
		{"subject", subject, "sub_6650aa8858310b9a0953079b5b014b7f39af1caa85eddc249e191a32eadae073"},
		{"v2", v2, "sha256:18fa80f76a79895ca78a750fb780018a55bd36ff4303d5ac5538007c9f9d0090"},
		{"v3", v3, "sha256:1e271e2808c776a2aaf6ea882b79cc419fb07d01c146f6601d0869491204a087"},
		{"arguments", HashBytes(args), "sha256:9b5187b19263b4d25cb2a98684f09d1c8734997dc560a7531076248537d5bdff"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %s", c.name, c.got)
		}
	}
}
