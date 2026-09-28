package obligation

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func testPolicy() domain.Phase3Policy {
	return domain.Phase3Policy{
		MaxPageSize: 2, MaxReceiptBytes: 1 << 20, MaxGCDecisions: 64,
		CheckpointGeneration: domain.GenerationDurable, CheckpointRetention: domain.RetentionHigh,
		Version: domain.Phase3PolicyVersion, Claim: ClaimPatternVersion, Matcher: MatcherRegistryVersion,
		ObservationState: ObservationStateRule, Eligibility: "eligibility/1", Locator: domain.ResourceLocatorEncodingV1,
		Coverage: "coverage/1", Dedup: "declaration/1",
		MaxOperations: 64, MaxMetadataBytes: 1 << 16, MaxTargets: 8, MaxEvidence: 8, MaxCoverageMembers: 64,
		MaxTransactionWork: 64, MaxToolResultBytes: 1 << 16, MaxCheckpointSemanticBytes: domain.DefaultMaxCheckpointSemanticBytes,
		DefaultLeaseCalls: 2, MaxLeaseCalls: 8, MaxLiveProofDependents: 6, GCTriggers: domain.DefaultGCTriggers(),
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(testPolicy(), DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func principal(a domain.Authority, task string) domain.Principal {
	return domain.Principal{SessionID: testSession, TaskID: task, Authority: a}
}

func TestNewPinsRuleVersions(t *testing.T) {
	for name, mod := range map[string]func(*domain.Phase3Policy){
		"claim":   func(p *domain.Phase3Policy) { p.Claim = "claim-pattern/v2" },
		"matcher": func(p *domain.Phase3Policy) { p.Matcher = "matcher-registry/v2" },
		"state":   func(p *domain.Phase3Policy) { p.ObservationState = "obs-state/2" },
	} {
		p := testPolicy()
		mod(&p)
		if _, err := New(p, DefaultRegistry()); !errors.Is(err, domain.ErrUnsupportedSchema) {
			t.Errorf("%s: New = %v, want ErrUnsupportedSchema", name, err)
		}
	}
	p := testPolicy()
	p.MaxTransactionWork = 0
	if _, err := New(p, DefaultRegistry()); err == nil {
		t.Error("unbounded policy accepted")
	}
	if _, err := New(testPolicy(), nil); err == nil {
		t.Error("nil registry accepted")
	}
	// The service keeps its own copy of the policy.
	p = testPolicy()
	s, err := New(p, DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	p.GCTriggers[0] = "MUTATED"
	if s.policy.GCTriggers[0] == "MUTATED" {
		t.Error("service aliases the caller's policy")
	}
}

type probeIntent struct {
	RequestID string
	Value     string
}

func TestReceiptReplayAndConflict(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	ctx := context.Background()
	actor := principal(domain.AuthorityHarness, "t1")
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "REEVALUATION", IDs: []string{"x1"}}}

	req, err := s.newRequest(domain.MutationObligationReevaluate, "r1", "Probe", probeIntent{RequestID: "r1", Value: "a"})
	if err != nil {
		t.Fatal(err)
	}
	err = st.Update(ctx, testSession, func(tx store.Tx) error {
		sem, err := begin(tx, actor, tx.NextSeq())
		if err != nil {
			return err
		}
		if _, ok, err := replay(tx, sem, actor, req); ok || err != nil {
			t.Fatalf("fresh request replayed: %v %v", ok, err)
		}
		return s.recordReceipt(tx, sem, actor, req, tx.LastSeq(), result)
	})
	if err != nil {
		t.Fatal(err)
	}

	check := func(actor domain.Principal, req request) (domain.MutationResult, bool, error) {
		var res domain.MutationResult
		var ok bool
		err := st.Update(ctx, testSession, func(tx store.Tx) error {
			sem, err := begin(tx, actor, tx.NextSeq())
			if err != nil {
				return err
			}
			res, ok, err = replay(tx, sem, actor, req)
			return err
		})
		return res, ok, err
	}
	got, ok, err := check(actor, req)
	if err != nil || !ok || got.Records.IDs[0] != "x1" {
		t.Fatalf("identical replay = %+v %v %v", got, ok, err)
	}
	changed, _ := s.newRequest(domain.MutationObligationReevaluate, "r1", "Probe", probeIntent{RequestID: "r1", Value: "b"})
	other := actor
	other.Authority = domain.AuthoritySystem
	renamed := req
	renamed.method = "Other"
	for name, c := range map[string]struct {
		actor domain.Principal
		req   request
	}{"arguments": {actor, changed}, "principal": {other, req}, "method": {actor, renamed}} {
		if _, _, err := check(c.actor, c.req); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Errorf("%s changed under same request ID: %v, want ErrEventIDConflict", name, err)
		}
	}
}

func TestBeginRejectsForeignSessionAndUnallocatedSeq(t *testing.T) {
	st := newTestStore(t)
	err := st.Update(context.Background(), testSession, func(tx store.Tx) error {
		if _, err := begin(tx, domain.Principal{SessionID: "s2", Authority: domain.AuthorityHarness}, tx.NextSeq()); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("foreign session: %v", err)
		}
		if _, err := begin(tx, principal(domain.AuthorityHarness, ""), tx.LastSeq()+5); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("unallocated seq: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
