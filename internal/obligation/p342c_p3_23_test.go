package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_23_ThreeLiveProofsAcrossTasksAndAgentPartitions (SPEC-6.8 P3-23,
// r6-spec6 line 145): the round-6 review of p342b's fan-out test — its TURN
// proof REPLACED task one's public proof on the same obligation, so only two
// proofs were ever live, and the private one was TURN-scoped, not
// agent-private. This is the corrected shape. THREE DISTINCT obligations of
// one session hold live proofs at the same time:
//
//   - task one's public tests proof (TASK boundary, matcher observation);
//   - a DISTINCT obligation of task one living in agent "agent"'s private
//     partition — its own pinned source and declaration — satisfied by a
//     resource-bound assertion whose proof carries the obligation's AGENT
//     boundary, read back from the store to prove it;
//   - task two's own-partition proof (own source, binding, declaration,
//     grant, run, and report).
//
// One edit to repo1 must reach all three through the one derived rule: every
// version settles UNRESOLVED with no current proof. Another agent cannot even
// name the agent-partition obligation. And the fan-out is not a wedge: both
// the public obligation and the agent partition re-satisfy at the new
// authoritative content, the agent's second proof again AGENT-scoped.
func TestP3_23_ThreeLiveProofsAcrossTasksAndAgentPartitions(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		agentBoundary := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "agent"}

		// Authoritative path content first, so every proof lands on the same
		// revision and fingerprint.
		f.resourceReport(t, "W1f", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})

		// Proof 1 — task one's public tests proof.
		f.matcherGrant(t, "g-23f-a", f.sysTests, TestsPassV1, f.system)
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1f"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("task-one public proof not established: %+v", o)
		}

		// Proof 2 — the agent partition's own obligation of task one: a
		// pinned source in agent "agent"'s boundary, a slot-1 declaration the
		// agent-side harness principal can see, and a resource-bound assertion.
		// The obligation inherits the source's AGENT boundary and the proof
		// inherits the obligation's, so the proof is AGENT-scoped for real.
		var agSrc domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			agSrc = storetest.NewDirective(testSession, "p-23ag", "dir23ag", tx.NextSeq(), "Read docs/a.md privately.")
			agSrc.Authority = domain.AuthorityUser
			agSrc.Scope = domain.ScopeAgent
			agSrc.TaskID, agSrc.AgentID = "task", "agent"
			agSrc.Access = agentBoundary
			if err := tx.InsertItem(agSrc); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, agSrc.ID)
		})
		ft := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
		decl := domain.DeclareObligationIntent{RequestID: "d-23ag", SourceItemID: agSrc.ID, DeclarationSlot: "1", Description: "read it privately",
			ExpectedSourceVersion: 1, Target: &ft, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, decl); err != nil {
			t.Fatalf("agent-partition declaration: %v", err)
		}
		agKey, _ := agSrc.CurrentKey()
		agSlot, _ := harnessSlot("1")
		refAg := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(agKey, agSlot), Version: 1}
		if o := f.status(t, refAg); o.BindingState != domain.BindingBound || o.Access != agentBoundary {
			t.Fatalf("agent-partition obligation = %+v, want BOUND in the agent boundary", o)
		}
		if err := f.assertPath(t, refAg, f.r.auth, "H1"); err != nil {
			t.Fatalf("agent-partition assertion: %v", err)
		}
		proofOf := func(ref domain.ObligationRef) domain.ApplicabilityProof {
			t.Helper()
			var p domain.ApplicabilityProof
			if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
				r, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				o, err := r.ExactObligation(ref)
				if err != nil {
					return err
				}
				p, err = r.ApplicabilityProof(o.CurrentProofID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return p
		}
		agProof := proofOf(refAg)
		if agProof.Access != agentBoundary || agProof.RuleVersion != ResourceAssertionRule || agProof.Target != refAg {
			t.Fatalf("agent-partition proof = %+v, want an AGENT-scoped resource assertion", agProof)
		}

		// Proof 3 — task two's own partition: own source, binding,
		// declaration, grant, run, and report.
		seedTask(t, f.st, "task2")
		h2 := f.harness
		h2.TaskID = "task2"
		task2Boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task2"}
		ws2 := bindIntent("ws2-23f", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task2"})
		ws2.Access = task2Boundary
		var t2src domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			t2src = storetest.NewDirective(testSession, "p-23t2f", "dir23t2f", tx.NextSeq(), "All tests must pass, thrice.")
			t2src.Authority = domain.AuthorityUser
			t2src.TaskID = "task2"
			t2src.Access = task2Boundary
			if err := tx.InsertItem(t2src); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, t2src.ID)
		})
		if _, err := f.s.bindWS(t, f.st, h2, ws2); err != nil {
			t.Fatalf("bind ws2-23f: %v", err)
		}
		if _, err := f.s.declare(t, f.st, h2, harnessDecl("d-23t2f", t2src.ID, 1, "1")); err != nil {
			t.Fatalf("task-two declaration: %v", err)
		}
		t2key, _ := t2src.CurrentKey()
		t2n, _ := harnessSlot("1")
		ref2 := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(t2key, t2n), Version: 1}
		if o := f.status(t, ref2); o.BindingState != domain.BindingBound {
			t.Fatalf("task-two obligation = %+v", o)
		}
		sys2 := f.system // a grant for task two's obligation is issued from task two's partition
		sys2.TaskID = "task2"
		f.matcherGrant(t, "g-23f-c", ref2, TestsPassV1, sys2)
		in2 := runIntent(fmt.Sprintf("run-23t2f-%d", runN+1), fmt.Sprintf("exec-23t2f-%d", runN+1), f.target)
		in2.TaskID = "task2"
		in2.Access, in2.Binding = task2Boundary, domain.WorkspaceBindingRef{ID: "ws2-23f", Version: 1}
		run2, err := f.registerRun(t, h2, in2)
		if err != nil {
			t.Fatalf("task-two run: %v", err)
		}
		runN++
		if _, err := f.observe(t, h2, obsIntent(fmt.Sprintf("obs-23t2f-%d", runN), run2, evidenceFor(t, f.st, run2).ID, domain.OutcomePass, hashOf("W1f"))); err != nil {
			t.Fatalf("task-two report: %v", err)
		}

		// Three proofs live on three DISTINCT obligations.
		live := map[string]string{}
		for name, ref := range map[string]domain.ObligationRef{
			"task one public": f.sysTests,
			"agent partition": refAg,
			"task two":        ref2,
		} {
			o := f.status(t, ref)
			if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
				t.Fatalf("%s holds no live proof: %+v", name, o)
			}
			live[ref.ObligationID] = o.CurrentProofID
		}
		if len(live) != 3 {
			t.Fatalf("obligations or proofs collide: %v", live)
		}

		// Agent privacy: another agent cannot even name the agent partition's
		// obligation — the access check answers ErrNotFound, nothing else.
		other := storetest.NewPrincipal(testSession, domain.AuthorityHarness)
		other.AgentID = "other"
		if _, err := f.s.transition(t, f.st, other, intent(refAg, f.status(t, refAg).Revision, domain.ObligationWaived)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("another agent reached the agent-partition obligation: %v", err)
		}

		// One edit. The one derived rule must reach all three live proofs.
		f.r.set(t, f.fixture, hashOf("W2"), false)
		f.wantInvalidated(t, f.sysTests, "repo1", "edit left task one's public proof live")
		f.wantSettled(t, refAg, "repo1", "edit left the AGENT-scoped proof live")
		f.wantSettled(t, ref2, "repo1", "edit left task two's proof live")
		for name, ref := range map[string]domain.ObligationRef{
			"task one": f.sysTests,
			"agent":    refAg,
			"task two": ref2,
		} {
			if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
				t.Fatalf("%s still carries a live proof after the edit: %+v", name, o)
			}
		}

		// Not a wedge: the public obligation and the agent partition both
		// re-satisfy at the new authoritative content, and the agent's second
		// proof is AGENT-scoped again and fresh.
		f.resourceReport(t, "W2f", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2f"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" || o.CurrentProofID == live[f.sysTests.ObligationID] {
			t.Fatalf("no fresh public proof after the fan-out: %+v", o)
		}
		if err := f.assertPath(t, refAg, f.r.auth, "H2"); err != nil {
			t.Fatalf("agent partition re-assertion: %v", err)
		}
		again := proofOf(refAg)
		if o := f.status(t, refAg); o.Status != domain.ObligationSatisfied || o.CurrentProofID == live[refAg.ObligationID] {
			t.Fatalf("no fresh agent-partition proof: %+v", o)
		}
		if again.Access != agentBoundary || again.RuleVersion != ResourceAssertionRule {
			t.Fatalf("re-established proof lost its AGENT scope: %+v", again)
		}
	})
}
