package domain

import (
	"errors"
	"testing"
)

// TestP3_3_SupersessionAuthorityMismatch closes the P3-42 table row
// "authority/boundary mismatch" (ADR 8 :1052): the boundary half is
// TestAuthorizeSupersession_DifferentAccessBoundariesFail; this is the
// authority half, for the agent-key supersession path P3-3 governs ("Agent
// first filing and supersession require AGENT_KEY, AGENT authority,
// identical task/agent/key and exact boundary"). Every case keeps the key,
// task, agent, session and access boundary EXACTLY equal and varies only
// authority, so the refusal can only come from the authority comparison:
// an actor below AGENT authority, a superseding keyed write that does not
// carry AGENT authority, and a superseding keyed write below the superseded
// item's authority are all refused with ErrInvalidAuthorityPromotion. The
// matching AGENT-actor control succeeds.
func TestP3_3_SupersessionAuthorityMismatch(t *testing.T) {
	// inAgentBoundary is agentActor with a different authority, so it still
	// satisfies the keyed items' task+agent access constraints and only the
	// authority comparison can refuse.
	inAgentBoundary := func(a Authority) Principal {
		return Principal{SessionID: "s1", TaskID: "t1", AgentID: "a1", Authority: a}
	}

	// Actor-side mismatch: TOOL (and RETRIEVED_CONTENT, its equal rank) can
	// never take the agent-key supersession path even with identical key,
	// task, agent and boundary.
	for _, actor := range []Principal{inAgentBoundary(AuthorityTool), inAgentBoundary(AuthorityRetrievedContent)} {
		if err := AuthorizeSupersession(actor, keyedAgentItem("status", taskBoundary), keyedAgentItem("status", taskBoundary)); !errors.Is(err, ErrInvalidAuthorityPromotion) {
			t.Errorf("actor %s: AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion (actor authority mismatch)", actor.Authority, err)
		}
	}

	// Item-side mismatch: the superseding keyed write itself must carry AGENT
	// authority; a USER-authority write over the same agent.<key> identity is
	// refused even for the owning agent.
	foreign := keyedAgentItem("status", taskBoundary)
	foreign.Authority = AuthorityUser
	if err := AuthorizeSupersession(agentActor(), foreign, keyedAgentItem("status", taskBoundary)); !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Errorf("non-AGENT keyed write: AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion (item authority mismatch)", err)
	}

	// Rank mismatch between the items: an AGENT-authority keyed write never
	// supersedes a keyed write of higher authority at the same boundary, even
	// for a SYSTEM actor that outranks both and can access both.
	higher := keyedAgentItem("status", taskBoundary)
	higher.Authority = AuthoritySystem
	if err := AuthorizeSupersession(inAgentBoundary(AuthoritySystem), keyedAgentItem("status", taskBoundary), higher); !errors.Is(err, ErrInvalidAuthorityPromotion) {
		t.Errorf("AGENT below SYSTEM keyed writes: AuthorizeSupersession() error = %v, want ErrInvalidAuthorityPromotion (superseding below superseded)", err)
	}

	// Control: with key, task, agent, boundary and AGENT authority all
	// matched, the same comparison authorizes (proving the refusals above
	// came from authority, not another dimension).
	if err := AuthorizeSupersession(agentActor(), keyedAgentItem("status", taskBoundary), keyedAgentItem("status", taskBoundary)); err != nil {
		t.Errorf("matched control: AuthorizeSupersession() error = %v, want nil", err)
	}
}
