package ingest

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/invocation"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Cross-package concurrency gate (INV-09/10/16): racing writers through
// ingest and the W3/W4 services on both stores always leave a state some
// serial order of the committed operations would produce.

const raceRounds = 6

// race runs fns at once and returns their errors in order.
func race(fns ...func() error) []error {
	errs := make([]error, len(fns))
	var start, done sync.WaitGroup
	start.Add(1)
	for i, fn := range fns {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = fn()
		}()
	}
	start.Done()
	done.Wait()
	return errs
}

// raceRound runs two operations for round i in one of three modes, so every
// test deterministically exercises both serial orders as well as a true race
// (TEST-1.4/1.5): the first runs to completion before the second, the second
// before the first, or both start at once behind race's barrier. Errors are
// returned in argument order.
func raceRound(i int, first, second func() error) []error {
	switch i % 3 {
	case 0:
		return []error{first(), second()}
	case 1:
		e2 := second()
		return []error{first(), e2}
	}
	return race(first, second)
}

// TestConcurrency_ReplacementVsTransition: replacing a pin while its v1
// obligation is being attested either retires an already SATISFIED v1 (the
// attestation committed first) or rejects the attestation of a retired
// version; the replacement always commits and v2 always starts UNRESOLVED.
func TestConcurrency_ReplacementVsTransition(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		sys := principal(domain.AuthoritySystem)
		attested := 0
		defer func() {
			t.Logf("attestation won %d of %d rounds", attested, raceRounds)
			// Both outcomes must occur, or the race exercised one branch only
			// (TEST-1.4).
			if !t.Failed() && (attested == 0 || attested == raceRounds) {
				t.Errorf("attestation won %d of %d rounds: one branch never exercised", attested, raceRounds)
			}
		}()
		for i := range raceRounds {
			key := fmt.Sprintf("dep%d", i)
			p1 := mustDirective(t, f.mustIngest(sys, sysEvent("v1-"+key, "## Pinned\n- ["+key+"] {obligation=tests_pass} Use version one.\n")), key)
			v1 := f.currentObligation(p1.ID)
			var p2 domain.IngestReceipt
			errs := raceRound(i,
				func() error {
					var err error
					p2, err = f.ingest(sys, sysEvent("v2-"+key, "## Pinned\n- ["+key+"] {obligation=tests_pass} Use version two.\n"))
					return err
				},
				func() error {
					_, err := f.ingest(sys, transitionEvent("sat-"+key, domain.EventSystem, v1, domain.ObligationSatisfied, domain.AssertionAttestation))
					return err
				})
			if errs[0] != nil {
				t.Fatalf("round %d: replacement failed: %v", i, errs[0])
			}
			if errs[1] == nil {
				attested++
			}
			v2 := f.currentObligation(mustDirective(t, p2, key).ID)
			if v2.Version != v1.Version+1 || v2.Status != domain.ObligationUnresolved {
				t.Fatalf("round %d: v2 = %+v", i, v2)
			}
			f.view(func(tx store.ReadTx) error {
				versions, err := tx.ObligationVersions(v1.ObligationID)
				if err != nil {
					return err
				}
				old := versions[0]
				want := domain.ObligationUnresolved
				if errs[1] == nil {
					want = domain.ObligationSatisfied
				}
				if old.Current || old.RetiredSeq == 0 || old.Status != want {
					t.Fatalf("round %d: v1 = %+v (attestation err %v)", i, old, errs[1])
				}
				trs, err := tx.ObligationTransitions(v1.ObligationID)
				for _, tr := range trs {
					if tr.Version == v1.Version && tr.Seq > old.RetiredSeq && tr.Cause == "" {
						t.Fatalf("round %d: v1 transitioned after retirement: %+v", i, tr)
					}
				}
				return err
			})
		}
	})
}

// TestConcurrency_CompletionVsCallReservation (P3-9 X8): CompleteTask racing
// a call reservation for the same task never leaves the task COMPLETED over
// a call reserved before completion; when completion loses the race it
// fails with ErrCallInFlight.
func TestConcurrency_CompletionVsCallReservation(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		if !hasCompletion(f.s) {
			t.Skip("GATE-PENDING: needs owner-goal and GC request facet records")
		}
		svc := f.lifecycleService()
		ledger := invocation.New(f.s)
		completed := 0
		defer func() {
			t.Logf("completion won %d of %d rounds", completed, raceRounds)
			// Both outcomes must occur (TEST-1.5).
			if !t.Failed() && (completed == 0 || completed == raceRounds) {
				t.Errorf("completion won %d of %d rounds: one branch never exercised", completed, raceRounds)
			}
		}()
		for i := range raceRounds {
			task := fmt.Sprintf("T%d", i)
			user := principal(domain.AuthorityUser)
			user.TaskID = task
			e := userEvent("open-"+task, "Start.", false)
			e.Spans[0].Access.TaskID = task
			f.mustIngest(user, e)
			sys, agent := principal(domain.AuthoritySystem), agentPrincipal()
			sys.TaskID, agent.TaskID = task, task
			seq := f.lastSeq()
			errs := raceRound(i,
				func() error {
					_, err := svc.CompleteTaskStandalone(ctx, sys, domain.CompleteTaskIntent{RequestID: "done-" + task, TaskID: task})
					return err
				},
				func() error {
					_, err := ledger.Prepare(ctx, invocation.PrepareRequest{Principal: agent, ServiceActor: dispatcherFor(agent), Operation: domain.OperationInference,
						BaseConversationVersion: 1, SemanticSeq: seq, Epoch: 1, PolicyVersion: "policy-1", DescriptorVersion: "desc-1",
						Request: []byte("r"), ManifestHash: domain.HashBytes([]byte("manifest r"))})
					return err
				})
			if errs[0] != nil && !errors.Is(errs[0], domain.ErrCallInFlight) {
				t.Fatalf("round %d: completion failed with %v", i, errs[0])
			}
			if errs[0] == nil {
				completed++
			}
			f.view(func(tx store.ReadTx) error {
				ts, err := tx.Task(task)
				if err != nil {
					return err
				}
				calls, err := tx.Calls(store.CallFilter{ConversationID: domain.ConversationIDFor(task, agent.AgentID)})
				if err != nil {
					return err
				}
				for _, c := range calls {
					inFlight := c.State == domain.CallPrepared || c.State == domain.CallSent || c.State == domain.CallUnknown
					if ts.Status == domain.TaskCompleted && inFlight && c.PreparedSeq < ts.CompletedSeq {
						t.Fatalf("round %d: task completed over in-flight call %+v", i, c)
					}
				}
				if (errs[0] == nil) != (ts.Status == domain.TaskCompleted) {
					t.Fatalf("round %d: completion err %v but task %s", i, errs[0], ts.Status)
				}
				return nil
			})
		}
	})
}

// TestConcurrency_InvalidationVsObservation (INV-16): a workspace report
// racing a PASS observation of the old or the new fingerprint never leaves
// a current SATISFIED proof whose fingerprint differs from the KNOWN
// resource state.
func TestConcurrency_InvalidationVsObservation(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		w := newT07(t, f, true, 1)
		fp := fingerprint("W1")
		satisfied, stored := 0, 0
		defer func() {
			t.Logf("observation left the proof current in %d of %d rounds; stored SATISFIED checked in %d", satisfied, raceRounds, stored)
		}()
		for i := range raceRounds {
			runID, exec, err := w.register(t07Target(nil))
			if err != nil {
				t.Fatal(err)
			}
			next := fingerprint(fmt.Sprintf("W%d", i+2))
			// Even rounds observe the fingerprint being replaced, odd rounds
			// the one being reported: that PASS may satisfy only when the
			// report commits first.
			observed := fp
			if i%2 == 1 {
				observed = next
			}
			errs := race(
				func() error { return w.reportAt(next, false, w.auth, w.auth+1) },
				func() error {
					return w.observe(runID, exec, observed, domain.OutcomePass, domain.ObservationComplete, taskAccess())
				})
			if errs[0] != nil {
				t.Fatalf("round %d: report failed: %v", i, errs[0])
			}
			fp = next
			f.view(func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				// INV-16 (K1 A5): stored SATISFIED is effective SATISFIED or
				// pending settlement, and effective SATISFIED rests on a
				// proof valid at read.
				o, err := sem.ExactObligation(w.ref)
				if err != nil {
					return err
				}
				eff, pending, err := obligation.EffectiveStatus(sem, o)
				if err != nil {
					return err
				}
				if o.Status == domain.ObligationSatisfied {
					stored++
					if eff != domain.ObligationSatisfied && !pending {
						t.Fatalf("round %d: stored SATISFIED is effectively %s without pending settlement", i, eff)
					}
					// A stored-SATISFIED RESOURCE_BOUND version rests on a
					// current proof; its mode is its satisfying transition's.
					mode, err := satisfiedMode(sem, w.ref)
					if err != nil {
						return err
					}
					if mode == domain.AssertionResourceBound && o.CurrentProofID == "" {
						t.Fatalf("round %d: stored SATISFIED RESOURCE_BOUND version has no current proof: %+v", i, o)
					}
				}
				if eff != domain.ObligationSatisfied {
					return nil
				}
				satisfied++
				p, err := sem.ApplicabilityProof(o.CurrentProofID)
				if err != nil {
					return err
				}
				rs, err := sem.ResourceState("repo1")
				if err != nil || rs.Freshness != domain.ResourceKnown || p.Fingerprint != rs.WorkspaceFingerprint {
					t.Fatalf("round %d: INV-16: proof at %s, resource %+v (%v)", i, p.Fingerprint, rs, err)
				}
				return nil
			})
		}
	})
}

// TestConcurrency_Collect (P3-38/39): two collections of the same task
// racing each other archive every candidate at most once, and each frozen
// receipt names only items it archived itself.
func TestConcurrency_Collect(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		if !hasCompletion(f.s) {
			t.Skip("GATE-PENDING: needs GC request and candidate facet records")
		}
		sys := principal(domain.AuthoritySystem)
		for i := range 4 {
			f.mustIngest(sys, sysEvent(fmt.Sprintf("w%d", i), fmt.Sprintf("## Working\n- step %d\n", i)))
		}
		svc := f.lifecycleService()
		var archived [2][]domain.ItemRevisionRef
		collect := func(req string, out *[]domain.ItemRevisionRef) error {
			return f.s.Update(ctx, sess, func(tx store.Tx) error {
				res, err := svc.Collect(tx, sys, domain.CollectIntent{RequestID: req, Scope: domain.CollectTask, TaskID: "T", Trigger: domain.GCManual}, tx.NextSeq())
				if err == nil && res.Result.Collect != nil {
					*out = res.Result.Collect.ArchivedRefs
				}
				return err
			})
		}
		errs := race(
			func() error { return collect("gc-a", &archived[0]) },
			func() error { return collect("gc-b", &archived[1]) })
		for n, err := range errs {
			if err != nil && !errors.Is(err, domain.ErrVersionConflict) {
				t.Fatalf("collector %d: %v", n, err)
			}
		}
		seen := map[string]int{}
		for _, refs := range archived {
			for _, ref := range refs {
				seen[ref.ItemID]++
			}
		}
		total := 0
		for id, n := range seen {
			total += n
			if n > 1 {
				t.Fatalf("%s archived by both collectors", id)
			}
			if it := f.item(id); it.Residency != domain.ResidencyArchived {
				t.Fatalf("%s reported archived but is %s", id, it.Residency)
			}
		}
		if total == 0 {
			t.Fatalf("no superseded Working item was collected; the race exercised nothing")
		}
	})
}

// satisfiedMode is the assertion mode of ref's latest transition to
// SATISFIED, or "" if it has none.
func satisfiedMode(sem store.SemanticReader, ref domain.ObligationRef) (domain.AssertionMode, error) {
	var mode domain.AssertionMode
	page := store.Page{Limit: 64}
	for {
		res, err := sem.TransitionsByVersion(ref, page)
		if err != nil {
			return "", err
		}
		for _, tr := range res.Records {
			if tr.To == domain.ObligationSatisfied {
				mode = tr.AssertionMode
			}
		}
		if !res.More {
			return mode, nil
		}
		page.After = res.Next
	}
}
