package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"testing"
)

func TestOutcomeEventIDIsNotAnOracle_SEC33(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		other := agent
		other.AgentID = "B"
		b := f.inference(agent, "r1")
		id, _ := domain.OutcomeEventID(b)
		probe := func(p domain.Principal) error {
			e := userEvent(id, "probe", false)
			if p.Authority == domain.AuthorityAgent {
				e = outcomeEvent(b, "probe")
			}
			_, err := f.ingest(p, e)
			return err
		}
		probers := []domain.Principal{other, principal(domain.AuthorityUser), principal(domain.AuthorityHarness)}
		var before []error
		for _, p := range probers {
			before = append(before, probe(p))
		}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "done"), &OutcomeMembership{Dispatcher: dispatcherFor(agent)}); err != nil {
			t.Fatalf("outcome: %v", err)
		}
		for i, p := range probers {
			after := probe(p)
			t.Logf("%s/%s: before outcome=%v after outcome=%v", p.Authority, p.AgentID, before[i], after)
			if before[i] == nil || after == nil || before[i].Error() != after.Error() {
				t.Errorf("ORACLE: %s learns whether the outcome event was recorded", p.Authority)
			}
		}
	})
}
