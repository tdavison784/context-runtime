package storetest

import (
	"encoding/json"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// CheckParity writes one scripted Phase 3 history through a store from each
// factory, calls reopen on the second store (a restart for a durable
// store), and requires both to read back the same logical state
// (FR-PER-001/003): items, relationships, obligation versions, transitions,
// proofs and their dependencies, current-proof and subject indexes,
// retrieval records, leases, and GC requests.
func CheckParity(t *testing.T, newA, newB func(t *testing.T) store.Store, reopen func(t *testing.T, s store.Store) store.Store) {
	t.Helper()
	a, b := newA(t), newB(t)
	t.Cleanup(func() { _ = a.Close() })
	for _, s := range []store.Store{a, b} {
		writeParityHistory(t, s)
	}
	b = reopen(t, b)
	t.Cleanup(func() { _ = b.Close() })
	ga, gb := parityState(t, a), parityState(t, b)
	ja, err := json.MarshalIndent(ga, "", " ")
	noErr(t, err)
	jb, err := json.MarshalIndent(gb, "", " ")
	noErr(t, err)
	if string(ja) != string(jb) {
		t.Errorf("stores disagree after the same history:\n first  %s\n second %s", ja, jb)
	}
}

// writeParityHistory builds the proof world, satisfies and then invalidates
// its obligation, retrieves a source, and files a GC request.
func writeParityHistory(t *testing.T, s store.Store) {
	t.Helper()
	o := proofWorld(t, s)
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		noErr(t, sem.InsertApplicabilityProof(proof, deps))
		tr, d := MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceUpdate(NewResourceUpdate(sessA, "u1", "repo", tx.NextSeq(), 0, fpB)))
		pid, err := domain.ApplicabilityProofID(Ref(o), "tr1")
		noErr(t, err)
		prior, err := readSemantic(t, tx).TransitionsByVersion(Ref(o), store.Page{Limit: 5})
		noErr(t, err)
		origin := &domain.OriginAuthorizationRef{TransitionID: "tr1", GrantID: "g-m", Actor: prior.Records[0].Actor, Target: Ref(o), Seq: prior.Records[0].Seq}
		seq := tx.NextSeq()
		tr := domain.ObligationTransition{ID: "tr2", SessionID: sessA, ObligationID: "o1", Version: 1, Seq: seq, From: domain.ObligationSatisfied,
			To: domain.ObligationUnresolved, Action: domain.ActionAssertObligation, Actor: HarnessPrincipal(sessA), Cause: domain.CauseResourceInvalidation,
			PriorProofID: pid, CauseRecordID: "u1", OriginAuthorizationRef: origin, RequestID: "req-tr2", ReasonCode: domain.ReasonResourceChanged}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-tr2", seq), Target: Ref(o), TransitionID: "tr2", Cause: domain.CauseResourceInvalidation,
			PreviousProofID: pid, ResourceUpdateID: "u1", OriginAuthorization: origin, RuleVersion: "rule/1"}
		_, err = sem.AppendSemanticObligationTransition(tr, d, 2)
		return err
	})
	var source domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		source = NewItem(sessA, "hist", tx.NextSeq(), "historical fact")
		source.Scope, source.Access = domain.ScopeAgent, AgentBoundary(sessA)
		return tx.InsertItem(source)
	})
	update(t, s, sessA, func(tx store.Tx) error { return newRetrievalBundle(t, source, "1", tx.NextSeq()).insert(t, tx) })
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertGCRequest(NewGCRequest(sessA, "gc1", tx.NextSeq()))
	})
}

type parityDump struct {
	LastSeq       uint64
	Items         []domain.ContextItem
	Relationships []domain.Relationship
	Obligation    domain.ObligationVersion
	Transitions   []domain.ObligationTransition
	Details       []domain.TransitionDetail
	Proofs        []domain.ApplicabilityProof
	Dependencies  []domain.ProofDependency
	CurrentProofs []domain.ApplicabilityProof
	Bound         []domain.ObligationVersion
	Satisfies     []domain.SatisfiesRelation
	Lease         domain.RetrievalLease
	Result        domain.RetrievalResult
	Projection    domain.ProjectionRecord
	Event         domain.RetrievalEvent
	Leases        []domain.RetrievalLease
	Pending       []domain.GCRequest
	Subject       []domain.SubjectState
}

func parityState(t *testing.T, s store.Store) parityDump {
	t.Helper()
	var d parityDump
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		page := store.Page{Limit: 50}
		var err error
		d.LastSeq = tx.LastSeq()
		d.Items, err = tx.Items(store.ItemFilter{})
		noErr(t, err)
		d.Relationships, err = tx.Relationships(store.RelationshipFilter{})
		noErr(t, err)
		if len(d.Relationships) == 0 {
			d.Relationships = nil // an empty list read may be nil or empty
		}
		ref := domain.ObligationRef{SessionID: sessA, ObligationID: "o1", Version: 1}
		d.Obligation, err = r.ExactObligation(ref)
		noErr(t, err)
		trs, err := r.TransitionsByVersion(ref, page)
		noErr(t, err)
		d.Transitions = trs.Records
		for _, tr := range trs.Records {
			det, err := r.TransitionDetail(tr.ID)
			noErr(t, err)
			d.Details = append(d.Details, det)
			if tr.ProofID != "" {
				p, err := r.ApplicabilityProof(tr.ProofID)
				noErr(t, err)
				d.Proofs = append(d.Proofs, p)
				deps, err := r.ProofDependencies(p.ID, page)
				noErr(t, err)
				d.Dependencies = append(d.Dependencies, deps.Records...)
			}
		}
		cur, err := r.CurrentProofsByDependency("repo", "", page)
		noErr(t, err)
		d.CurrentProofs = cur.Records
		bound, err := r.CurrentBoundObligationsBySubject(d.Obligation.TargetSubjectKey, page)
		noErr(t, err)
		d.Bound = bound.Records
		sat, err := r.Satisfies(AgentPrincipal(sessA, "task", "agent"), ref, false, page)
		noErr(t, err)
		d.Satisfies = sat.Records
		d.Lease, err = r.RetrievalLease("lease-1")
		noErr(t, err)
		d.Result, err = r.RetrievalResult("res-1")
		noErr(t, err)
		d.Projection, err = r.Projection("pr-1")
		noErr(t, err)
		d.Event, err = r.RetrievalEvent("rev-1")
		noErr(t, err)
		leases, err := r.LeasesBySource(d.Lease.Source, page)
		noErr(t, err)
		d.Leases = leases.Records
		pending, err := r.PendingGCRequests(page)
		noErr(t, err)
		d.Pending = pending.Records
		subjects, err := r.SubjectStatesByResource("repo", page)
		noErr(t, err)
		d.Subject = subjects.Records
		return nil
	})
	return d
}
