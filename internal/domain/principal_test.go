package domain

import (
	"errors"
	"math/rand"
	"testing"
)

func TestPrincipalValidate(t *testing.T) {
	cases := []struct {
		name    string
		p       Principal
		wantErr bool
	}{
		{"valid", Principal{SessionID: "s1", Authority: AuthoritySystem}, false},
		{"missing session", Principal{Authority: AuthoritySystem}, true},
		{"invalid authority", Principal{SessionID: "s1", Authority: "BOGUS"}, true},
		{"missing everything", Principal{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidRecord) {
				t.Errorf("Validate() error %v does not wrap ErrInvalidRecord", err)
			}
		})
	}
}

// TestBoundaryFor checks BoundaryFor sets exactly the owner field its scope
// requires, from the principal's matching field, and never leaks the other
// owner fields.
func TestBoundaryFor(t *testing.T) {
	p := Principal{SessionID: "sess", WorkflowID: "wf", TaskID: "task", AgentID: "agent"}

	cases := []struct {
		scope Scope
		want  AccessBoundary
	}{
		{ScopeTurn, AccessBoundary{Scope: ScopeTurn, SessionID: "sess", TaskID: "task"}},
		{ScopeTask, AccessBoundary{Scope: ScopeTask, SessionID: "sess", TaskID: "task"}},
		{ScopeWorkflow, AccessBoundary{Scope: ScopeWorkflow, SessionID: "sess", WorkflowID: "wf"}},
		{ScopeAgent, AccessBoundary{Scope: ScopeAgent, SessionID: "sess", AgentID: "agent"}},
		{ScopeSession, AccessBoundary{Scope: ScopeSession, SessionID: "sess"}},
	}
	for _, c := range cases {
		if got := BoundaryFor(c.scope, p); got != c.want {
			t.Errorf("BoundaryFor(%s, p) = %+v, want %+v", c.scope, got, c.want)
		}
	}
}

func TestAccessBoundaryValidate(t *testing.T) {
	cases := []struct {
		name    string
		b       AccessBoundary
		wantErr bool
	}{
		{"turn ok", AccessBoundary{Scope: ScopeTurn, SessionID: "s", TaskID: "t"}, false},
		{"turn missing task", AccessBoundary{Scope: ScopeTurn, SessionID: "s"}, true},
		{"task ok", AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"}, false},
		{"task missing task", AccessBoundary{Scope: ScopeTask, SessionID: "s"}, true},
		{"workflow ok", AccessBoundary{Scope: ScopeWorkflow, SessionID: "s", WorkflowID: "w"}, false},
		{"workflow missing workflow", AccessBoundary{Scope: ScopeWorkflow, SessionID: "s"}, true},
		{"agent ok", AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a"}, false},
		{"agent missing agent", AccessBoundary{Scope: ScopeAgent, SessionID: "s"}, true},
		{"session ok, no owners needed", AccessBoundary{Scope: ScopeSession, SessionID: "s"}, false},
		{"session ok with extra conjunctive owners", AccessBoundary{Scope: ScopeSession, SessionID: "s", TaskID: "t", AgentID: "a"}, false},
		{"missing session id", AccessBoundary{Scope: ScopeTask, TaskID: "t"}, true},
		{"invalid scope", AccessBoundary{Scope: "BOGUS", SessionID: "s"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.b.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// TestAccessBoundaryPermits checks FR-DOM-003: access always requires the
// same session, plus every non-empty owner constraint the boundary carries
// (a conjunction), across same/different session, task, workflow, and agent.
func TestAccessBoundaryPermits(t *testing.T) {
	cases := []struct {
		name string
		b    AccessBoundary
		p    Principal
		want bool
	}{
		{
			"same session same task: task scope permits",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			Principal{SessionID: "s", TaskID: "t1"},
			true,
		},
		{
			"same session different task: task scope denies",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			Principal{SessionID: "s", TaskID: "t2"},
			false,
		},
		{
			"different session: always denies regardless of other owners",
			AccessBoundary{Scope: ScopeSession, SessionID: "s1"},
			Principal{SessionID: "s2"},
			false,
		},
		{
			"session scope permits any task/workflow/agent in same session",
			AccessBoundary{Scope: ScopeSession, SessionID: "s"},
			Principal{SessionID: "s", TaskID: "irrelevant", WorkflowID: "irrelevant", AgentID: "irrelevant"},
			true,
		},
		{
			"workflow scope: same workflow permits",
			AccessBoundary{Scope: ScopeWorkflow, SessionID: "s", WorkflowID: "w1"},
			Principal{SessionID: "s", WorkflowID: "w1"},
			true,
		},
		{
			"workflow scope: different workflow denies",
			AccessBoundary{Scope: ScopeWorkflow, SessionID: "s", WorkflowID: "w1"},
			Principal{SessionID: "s", WorkflowID: "w2"},
			false,
		},
		{
			"agent scope: same agent permits",
			AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a1"},
			Principal{SessionID: "s", AgentID: "a1"},
			true,
		},
		{
			"agent scope: different agent denies",
			AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a1"},
			Principal{SessionID: "s", AgentID: "a2"},
			false,
		},
		{
			"conjunctive task+agent: both must match",
			AccessBoundary{Scope: ScopeSession, SessionID: "s", TaskID: "t1", AgentID: "a1"},
			Principal{SessionID: "s", TaskID: "t1", AgentID: "a1"},
			true,
		},
		{
			"conjunctive task+agent: task matches but agent differs denies",
			AccessBoundary{Scope: ScopeSession, SessionID: "s", TaskID: "t1", AgentID: "a1"},
			Principal{SessionID: "s", TaskID: "t1", AgentID: "a2"},
			false,
		},
		{
			"conjunctive task+agent: agent matches but task differs denies",
			AccessBoundary{Scope: ScopeSession, SessionID: "s", TaskID: "t1", AgentID: "a1"},
			Principal{SessionID: "s", TaskID: "t2", AgentID: "a1"},
			false,
		},
		{
			"invalid boundary never permits",
			AccessBoundary{Scope: ScopeTask, SessionID: "s"}, // missing required TaskID
			Principal{SessionID: "s"},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.b.Permits(c.p); got != c.want {
				t.Errorf("Permits() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestAccessBoundaryWithin checks derived content is Within a source boundary
// exactly when it shares the source's session and carries every owner
// constraint the source carries (FR-REL-008).
func TestAccessBoundaryWithin(t *testing.T) {
	cases := []struct {
		name  string
		inner AccessBoundary
		outer AccessBoundary
		want  bool
	}{
		{
			"equal boundaries are within each other",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"},
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"},
			true,
		},
		{
			"narrower (extra agent constraint) is within broader task-only",
			AccessBoundary{Scope: ScopeAgent, SessionID: "s", TaskID: "t", AgentID: "a"},
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"},
			true,
		},
		{
			"broader task-only is NOT within narrower task+agent",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"},
			AccessBoundary{Scope: ScopeAgent, SessionID: "s", TaskID: "t", AgentID: "a"},
			false,
		},
		{
			"different session is never within",
			AccessBoundary{Scope: ScopeSession, SessionID: "s1"},
			AccessBoundary{Scope: ScopeSession, SessionID: "s2"},
			false,
		},
		{
			"different task is not within",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t2"},
			false,
		},
		{
			"session-scoped outer with no owners is within by anything in session",
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"},
			AccessBoundary{Scope: ScopeSession, SessionID: "s"},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.inner.Within(c.outer); got != c.want {
				t.Errorf("Within() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIntersect(t *testing.T) {
	cases := []struct {
		name    string
		a, b    AccessBoundary
		scope   Scope
		wantOK  bool
		wantOut AccessBoundary
	}{
		{
			name:    "disjoint owners merge (task from a, agent from b)",
			a:       AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			b:       AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a1"},
			scope:   ScopeSession,
			wantOK:  true,
			wantOut: AccessBoundary{Scope: ScopeSession, SessionID: "s", TaskID: "t1", AgentID: "a1"},
		},
		{
			name:   "same task on both sides ok",
			a:      AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			b:      AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			scope:  ScopeTask,
			wantOK: true, wantOut: AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
		},
		{
			name:   "different tasks: no principal could satisfy both",
			a:      AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			b:      AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t2"},
			scope:  ScopeTask,
			wantOK: false,
		},
		{
			name:   "different sessions never intersect",
			a:      AccessBoundary{Scope: ScopeSession, SessionID: "s1"},
			b:      AccessBoundary{Scope: ScopeSession, SessionID: "s2"},
			scope:  ScopeSession,
			wantOK: false,
		},
		{
			name:   "resulting boundary must still satisfy requested scope's minimum",
			a:      AccessBoundary{Scope: ScopeSession, SessionID: "s"},
			b:      AccessBoundary{Scope: ScopeSession, SessionID: "s"},
			scope:  ScopeTask, // requires TaskID but neither side has one
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ok := Intersect(c.scope, c.a, c.b)
			if ok != c.wantOK {
				t.Fatalf("Intersect() ok = %v, want %v (out=%+v)", ok, c.wantOK, out)
			}
			if ok && out != c.wantOut {
				t.Errorf("Intersect() = %+v, want %+v", out, c.wantOut)
			}
		})
	}
}

// TestIntersectCommutative checks Intersect(scope, a, b) and
// Intersect(scope, b, a) agree, for both successful and failing pairs.
func TestIntersectCommutative(t *testing.T) {
	pairs := []struct{ a, b AccessBoundary }{
		{
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a1"},
		},
		{
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"},
			AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t2"},
		},
		{
			AccessBoundary{Scope: ScopeSession, SessionID: "s1"},
			AccessBoundary{Scope: ScopeSession, SessionID: "s2"},
		},
	}
	for _, p := range pairs {
		outAB, okAB := Intersect(ScopeSession, p.a, p.b)
		outBA, okBA := Intersect(ScopeSession, p.b, p.a)
		if okAB != okBA {
			t.Fatalf("Intersect not commutative on ok: (a,b)=%v (b,a)=%v", okAB, okBA)
		}
		if okAB && outAB != outBA {
			t.Errorf("Intersect not commutative on result: (a,b)=%+v (b,a)=%+v", outAB, outBA)
		}
	}
}

// TestIntersectResultWithinBoth checks a handful of fixed successful
// intersections are Within both inputs, ahead of the broader property test
// below.
func TestIntersectResultWithinBoth(t *testing.T) {
	a := AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t1"}
	b := AccessBoundary{Scope: ScopeAgent, SessionID: "s", AgentID: "a1"}
	out, ok := Intersect(ScopeSession, a, b)
	if !ok {
		t.Fatal("Intersect() ok = false, want true")
	}
	if !out.Within(a) {
		t.Errorf("Intersect result %+v is not Within a %+v", out, a)
	}
	if !out.Within(b) {
		t.Errorf("Intersect result %+v is not Within b %+v", out, b)
	}
}

// TestAccessBoundaryProperty is a seeded randomized check over generated
// boundaries and principals: whenever Intersect succeeds and the requested
// scope's minimums are met by the merged owners, the result must be Within
// both inputs, and for every generated principal,
// result.Permits(p) == a.Permits(p) && b.Permits(p).
//
// The generated inputs a and b are themselves always individually valid
// (each carries the owner its own scope requires), matching every real
// AccessBoundary a store would hold: Permits calls b.Validate() internally,
// so an input that violates its own scope's minimum is not a boundary that
// could exist in the system, and the equivalence does not hold against it
// (Intersect does not itself re-validate its inputs, only its output).
func TestAccessBoundaryProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20260925))

	sessions := []string{"s1", "s2"}
	owners := []string{"", "x", "y"} // "" means unconstrained on that field
	nonEmpty := []string{"x", "y"}   // used to satisfy a scope's own minimum
	scopes := []Scope{ScopeTurn, ScopeTask, ScopeWorkflow, ScopeSession, ScopeAgent}

	randBoundary := func() AccessBoundary {
		scope := scopes[rng.Intn(len(scopes))]
		b := AccessBoundary{
			Scope:      scope,
			SessionID:  sessions[rng.Intn(len(sessions))],
			WorkflowID: owners[rng.Intn(len(owners))],
			TaskID:     owners[rng.Intn(len(owners))],
			AgentID:    owners[rng.Intn(len(owners))],
		}
		// Force the field the scope itself requires so b.Validate() succeeds;
		// every other field stays free to be empty or set.
		switch scope {
		case ScopeTurn, ScopeTask:
			if b.TaskID == "" {
				b.TaskID = nonEmpty[rng.Intn(len(nonEmpty))]
			}
		case ScopeWorkflow:
			if b.WorkflowID == "" {
				b.WorkflowID = nonEmpty[rng.Intn(len(nonEmpty))]
			}
		case ScopeAgent:
			if b.AgentID == "" {
				b.AgentID = nonEmpty[rng.Intn(len(nonEmpty))]
			}
		}
		return b
	}
	randPrincipal := func() Principal {
		return Principal{
			SessionID:  sessions[rng.Intn(len(sessions))],
			WorkflowID: owners[rng.Intn(len(owners))],
			TaskID:     owners[rng.Intn(len(owners))],
			AgentID:    owners[rng.Intn(len(owners))],
			Authority:  AuthoritySystem,
		}
	}

	const iterations = 2000
	checked := 0
	for i := 0; i < iterations; i++ {
		a := randBoundary()
		b := randBoundary()
		if a.Validate() != nil || b.Validate() != nil {
			t.Fatalf("generator produced an individually invalid boundary: a=%+v b=%+v", a, b)
		}
		scope := scopes[rng.Intn(len(scopes))]

		out, ok := Intersect(scope, a, b)
		if !ok {
			continue // Intersect itself already rejected an unsatisfiable pair
		}
		checked++
		if !out.Within(a) {
			t.Fatalf("Intersect(%s, %+v, %+v) = %+v not Within a", scope, a, b, out)
		}
		if !out.Within(b) {
			t.Fatalf("Intersect(%s, %+v, %+v) = %+v not Within b", scope, a, b, out)
		}
		for j := 0; j < 5; j++ {
			p := randPrincipal()
			want := a.Permits(p) && b.Permits(p)
			got := out.Permits(p)
			if got != want {
				t.Fatalf("Intersect(%s, %+v, %+v) = %+v: Permits(%+v) = %v, want a.Permits&&b.Permits = %v",
					scope, a, b, out, p, got, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("property test never found a satisfiable Intersect pair; generator is too narrow")
	}
	t.Logf("checked %d/%d satisfiable Intersect pairs", checked, iterations)
}

func TestSourceActorReducesAuthority(t *testing.T) {
	caller := Principal{SessionID: "s1", WorkflowID: "w1", TaskID: "t1", AgentID: "a1", Authority: AuthoritySystem}
	actor, err := SourceActor(caller, AuthorityUser)
	if err != nil {
		t.Fatal(err)
	}
	want := caller
	want.Authority = AuthorityUser
	if actor != want {
		t.Fatalf("SourceActor = %+v, want %+v", actor, want)
	}
	for _, c := range []struct {
		caller, span Authority
	}{
		{AuthorityUser, AuthorityHarness},
		{AuthorityTool, AuthorityRetrievedContent},
		{AuthorityRetrievedContent, AuthorityTool},
		{AuthoritySystem, "bogus"},
	} {
		p := caller
		p.Authority = c.caller
		if _, err := SourceActor(p, c.span); !errors.Is(err, ErrInvalidAuthorityPromotion) {
			t.Errorf("SourceActor(%s, %s) err = %v, want ErrInvalidAuthorityPromotion", c.caller, c.span, err)
		}
	}
	if _, err := SourceActor(Principal{Authority: AuthoritySystem}, AuthorityUser); err == nil {
		t.Fatal("invalid caller accepted")
	}
}
