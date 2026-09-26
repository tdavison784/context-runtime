package directive_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
)

// These tests keep the parser (syntax-level attribute validation) and
// policy.ForDirective (ingestion's fail-closed re-check) from drifting apart
// silently (R16, R17): everything the parser accepts, policy accepts with the
// same values, and single-attribute rejection reasons agree exactly.

var (
	sections   = []string{"Goal", "Pinned", "Working", "Remember", "References", "Ephemeral"}
	parsers    = []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser}
	attrValues = map[string][]string{
		"kind": {"goal", "constraint", "instruction", "Instruction", "task_state", "conversation", "fact", "decision",
			"summary", "reference", "evidence", "tool_result", "user_message", "CONSTRAINT", "x"},
		"scope":      {"TURN", "TASK", "WORKFLOW", "SESSION", "AGENT", "turn", "Task", "session", "GLOBAL"},
		"ttl":        {"1", "2", "0002", "0", "000", "-1", "1e3", "2147483647", "99999999999x", "x"},
		"obligation": {"tests_pass", "a.b-c", "X"},
		"Kind":       {"constraint"},
		"unknown":    {"x"},
	}
)

func overrides(it directive.Item) policy.Overrides {
	return policy.Overrides{Kind: it.Kind, Scope: it.Scope, TTLTurns: it.TTLTurns, Obligation: it.Obligation}
}

// assertPolicyAccepts fails when policy rejects or rewrites parser output.
func assertPolicyAccepts(t *testing.T, input string, r directive.Result, authority domain.Authority) {
	t.Helper()
	for _, it := range r.Items {
		d, ttl, err := policy.ForDirective(it.Section, authority, overrides(it))
		if err != nil {
			t.Fatalf("%q: parser accepted %+v, policy rejected: %v", input, overrides(it), err)
		}
		if it.Kind != "" && d.Kind != it.Kind || it.Scope != "" && d.Scope != it.Scope || (ttl == nil) != (it.TTLTurns == 0) || ttl != nil && *ttl != it.TTLTurns {
			t.Fatalf("%q: policy changed parser metadata: %+v ttl=%v vs %+v", input, d, ttl, overrides(it))
		}
	}
}

func TestParserAcceptedImpliesPolicyAccepted(t *testing.T) {
	for _, section := range sections {
		for _, authority := range parsers {
			for name, values := range attrValues {
				for _, value := range values {
					attr := name + "=" + value
					for _, input := range []string{"## " + section + " " + attr + "\ntext", "## " + section + "\n- {" + attr + "} text"} {
						r := directive.Parse([]byte(input), directive.Options{Authority: authority, DirectiveCapable: true})
						if r.Err != nil || len(r.Items) != 1 {
							t.Fatalf("%q: %+v", input, r)
						}
						assertPolicyAccepts(t, input, r, authority)
						// Exact agreement on the single attribute's outcome.
						var parserReason domain.DiagnosticReason
						for _, d := range r.Diagnostics {
							if d.Code == domain.ErrMalformedDirective {
								parserReason = d.Reason
							}
						}
						_, policyReason, err := policy.ValidateAttribute(domain.DirectiveSection(strings.ToUpper(section)), authority, name, value)
						if err != nil || parserReason != policyReason {
							t.Errorf("%s %s %q: parser reason %q, policy reason %q (err %v)", section, authority, attr, parserReason, policyReason, err)
						}
					}
				}
			}
		}
	}
}

// TestRepresentationLimitAgreement: both sides reject an oversized allowed
// ttl as a whole-event validation error rather than ignoring it (R1).
func TestRepresentationLimitAgreement(t *testing.T) {
	for _, value := range []string{"2147483648", "000099999999999999999999"} {
		r := directive.Parse([]byte("## Working ttl="+value+"\n- a"), directive.Options{Authority: domain.AuthoritySystem})
		_, _, err := policy.ValidateAttribute(domain.SectionWorking, domain.AuthoritySystem, "ttl", value)
		if !errors.Is(r.Err, directive.ErrRepresentationLimit) || !errors.Is(err, policy.ErrTTLRepresentation) || !errors.Is(r.Err, domain.ErrInvalidRecord) || !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("%s: parser %v, policy %v", value, r.Err, err)
		}
	}
}

// FuzzPolicyAgreement extends the cross-check to arbitrary parser input.
func FuzzPolicyAgreement(f *testing.F) {
	f.Add([]byte("## Pinned kind=instruction scope=SESSION obligation=x\n- {kind=constraint scope=TURN} a\n## Ephemeral ttl=0003\n- {kind=tool_result} b"), uint8(0))
	f.Add([]byte("## Remember scope=WORKFLOW\n- {ttl=2 kind=summary} a"), uint8(2))
	f.Fuzz(func(t *testing.T, input []byte, source uint8) {
		if len(input) > 64<<10 {
			t.Skip()
		}
		authority := parsers[int(source)%len(parsers)]
		r := directive.Parse(input, directive.Options{Authority: authority, DirectiveCapable: true})
		assertPolicyAccepts(t, string(input), r, authority)
	})
}
