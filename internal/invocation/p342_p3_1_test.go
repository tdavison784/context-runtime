package invocation

import (
	"sort"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p31fp is storetest's workspace-A fingerprint, rebuilt locally: the
// unexported original is not visible from this package.
var p31fp = domain.HashBytes([]byte("workspace A"))

// p31Sem returns tx's Phase 3 facet, the transaction-scoped way every family
// below writes its records.
func p31Sem(t *testing.T, tx store.Tx) store.SemanticTx {
	t.Helper()
	sem, err := store.Semantic(tx)
	if err != nil {
		t.Fatalf("store.Semantic: %v", err)
	}
	return sem
}

// p31Update commits fn on s in this package's session.
func p31Update(t *testing.T, s store.Store, fn func(tx store.Tx) error) {
	t.Helper()
	must(t, s.Update(ctx, sess, fn))
}

// p31PutTask creates ACTIVE task "task", the prerequisite of run, GC, and
// observation records.
func p31PutTask(t *testing.T, tx store.Tx) {
	t.Helper()
	_, err := tx.PutTask(storetest.NewTask(tx.SessionID(), "task"), 0,
		storetest.NewLifecycleEvent(tx.SessionID(), "task-create", tx.NextSeq(), domain.TargetTask, "task"))
	if err != nil {
		t.Fatalf("PutTask: %v", err)
	}
}

// p31SignCoverage keys, sorts, and signs a coverage record and its members
// the way storetest's unexported signCoverage does, for the member shapes
// NewCoverage cannot build (exchange and lease members).
func p31SignCoverage(t *testing.T, c domain.CoverageRecord, members []domain.CoverageMember) (domain.CoverageRecord, []domain.CoverageMember) {
	t.Helper()
	for i := range members {
		key, err := members[i].Key()
		if err != nil {
			t.Fatal(err)
		}
		members[i].ID = key
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	c.MemberCount = uint64(len(members))
	sig, err := domain.CoverageSignature(c, members)
	if err != nil {
		t.Fatal(err)
	}
	c.Signature = sig
	return c, members
}

// p31Family is one semantic record family: setup commits the family's
// prerequisites before the ledger reserves, and write is the family's own
// committed write afterwards. control marks the negative control that writes
// nothing and must NOT go stale.
type p31Family struct {
	name    string
	control bool
	setup   func(t *testing.T, s store.Store)
	write   func(t *testing.T, tx store.Tx) error
}

// p31BoundWorld stores proofWorld's bound obligation through op: task, the
// repo binding, workspace binding "wb", a PASS run with TOOL evidence and
// evidence coverage, the source directive, and obligation o1 v1; the
// declaration and matcher grant are optional so one family can carry them as
// its own write or its prerequisite.
func p31BoundWorld(op *domain.ObligationVersion, withDeclaration, withGrant bool) func(t *testing.T, s store.Store) {
	return func(t *testing.T, s store.Store) {
		p31Update(t, s, func(tx store.Tx) error {
			sem := p31Sem(t, tx)
			p31PutTask(t, tx)
			if err := sem.InsertResourceBinding(storetest.NewResourceBinding(sess, "repo", tx.NextSeq())); err != nil {
				return err
			}
			if err := sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding(sess, "wb", "repo", 1, tx.NextSeq())); err != nil {
				return err
			}
			run := storetest.NewObservationRun(t, sess, "run1", "repo", "wb", tx.NextSeq())
			run.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "task"}
			if err := sem.InsertObservationRun(run); err != nil {
				return err
			}
			ev := storetest.ProducedEvidence(sess, "ev1", tx.NextSeq(), run.ExecutionID)
			ev.Access = run.Access
			if err := tx.InsertItem(ev); err != nil {
				return err
			}
			if err := sem.InsertObservation(storetest.NewObservation(run, "obs1", "ev1", tx.NextSeq(), p31fp)); err != nil {
				return err
			}
			cov, members := storetest.NewCoverage(t, sess, "evcov", tx.NextSeq(), domain.CoverageEvidenceSupport, storetest.ContentRef(ev))
			if err := sem.InsertCoverage(cov, members); err != nil {
				return err
			}
			if err := tx.InsertItem(storetest.SemanticDirective(sess, "src", "dep", tx.NextSeq(), "All tests must pass")); err != nil {
				return err
			}
			o := storetest.BoundObligation(t, sess, "o1", 1, tx.NextSeq(), "src")
			o.Access = run.Access
			if err := tx.InsertObligationVersion(o); err != nil {
				return err
			}
			*op = o
			if withDeclaration {
				if err := sem.InsertObligationDeclaration(storetest.DeclarationOf(o, tx.NextSeq())); err != nil {
					return err
				}
			}
			if withGrant {
				g := storetest.NewGrant(sess, "g-m", tx.NextSeq())
				g.Action, g.TargetIDs, g.Targets = domain.ActionAssertObligation, nil, []domain.GrantTarget{storetest.Ref(o).Target()}
				g.Grantee, g.Issuer, g.Matcher = nil, storetest.NewPrincipal(sess, domain.AuthoritySystem), o.Matcher
				return tx.InsertGrant(g)
			}
			return nil
		})
	}
}

// p31Families enumerates every Phase 3 semantic record family the store's
// semantic facet can write (store/semantic.go's six writer interfaces), plus
// the obligation version itself, which rides the core store. Each family is
// proven against the storetest conformance recipes; the two unsequenced
// operational Puts (PutGCProgress, PutGCQueueCursor) are deliberately absent:
// their interface contract states they are unsequenced operational state, so
// they consume no sequence and cannot make a reservation stale.
func p31Families() []p31Family {
	var fams []p31Family

	// Control: with nothing written after Prepare, MarkSent succeeds — the
	// staleness below is caused by each family's write, not by Prepare.
	fams = append(fams, p31Family{name: "control-nothing", control: true})

	// Obligation version (core store write of a Phase 3 record).
	fams = append(fams, p31Family{
		name: "obligation-version",
		write: func(t *testing.T, tx store.Tx) error {
			return tx.InsertObligationVersion(storetest.NewObligation(sess, "o1", 1, tx.NextSeq(), "src"))
		},
	})

	// DeclarationWriter.
	var pin domain.ContextItem
	fams = append(fams, p31Family{
		name: "creation-declaration",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				pin = storetest.SemanticDirective(sess, "p1", "dep", tx.NextSeq(), "run tests")
				return tx.InsertItem(pin)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertCreationDeclaration(storetest.CreationDeclarationFor(t, pin, "decl-p1", tx.NextSeq(), nil, nil))
		},
	})
	var declA, declB domain.CreationDeclaration
	var itemA domain.ContextItem
	fams = append(fams, p31Family{
		name: "snapshot-declaration",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				sem := p31Sem(t, tx)
				itemA = storetest.SemanticDirective(sess, "a", "a", tx.NextSeq(), "a")
				itemB := storetest.SemanticDirective(sess, "b", "b", tx.NextSeq(), "b")
				if err := tx.InsertItem(itemA); err != nil {
					return err
				}
				if err := tx.InsertItem(itemB); err != nil {
					return err
				}
				declA, declB = storetest.CreationDeclarationFor(t, itemA, "da", tx.NextSeq(), nil, nil),
					storetest.CreationDeclarationFor(t, itemB, "db", tx.NextSeq(), nil, nil)
				if err := sem.InsertCreationDeclaration(declA); err != nil {
					return err
				}
				return sem.InsertCreationDeclaration(declB)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			seq := tx.NextSeq()
			sd := domain.SnapshotDeclaration{SemanticMeta: storetest.Meta(sess, "snap", seq), TaskID: "task",
				Authority: itemA.Authority, Access: itemA.Access, PolicyVersion: domain.Phase3PolicyVersion, LegacyKnown: true,
				Members: []domain.SnapshotDeclarationMember{
					{ItemID: declB.ItemID, DeclarationID: declB.ID, Signature: declB.Signature},
					{ItemID: declA.ItemID, DeclarationID: declA.ID, Signature: declA.Signature},
				}}
			sig, err := sd.CanonicalSignature()
			if err != nil {
				return err
			}
			sd.Signature = sig
			return p31Sem(t, tx).InsertSnapshotDeclaration(sd)
		},
	})
	var covA, covB domain.ContextItem
	fams = append(fams, p31Family{
		name: "coverage",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				covA, covB = storetest.NewItem(sess, "a", tx.NextSeq(), "a"), storetest.NewItem(sess, "b", tx.NextSeq(), "b")
				if err := tx.InsertItem(covA); err != nil {
					return err
				}
				return tx.InsertItem(covB)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			c, members := storetest.NewCoverage(t, sess, "cov", tx.NextSeq(), domain.CoverageProvenance, storetest.ContentRef(covA), storetest.ContentRef(covB))
			return p31Sem(t, tx).InsertCoverage(c, members)
		},
	})
	fams = append(fams, p31Family{
		name: "current-version-pointer",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				return tx.InsertItem(storetest.SemanticDirective(sess, "p1", "dep", tx.NextSeq(), "p1"))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			// The pointer ride-alongs with a sequenced record of the same
			// write, as every production filing does; alone it is refused.
			if err := tx.AppendLifecycleEvent(storetest.NewItemEvent(sess, "l-pin", tx.NextSeq(), "p1")); err != nil {
				return err
			}
			return p31Sem(t, tx).SetCurrentVersion("p1", "")
		},
	})
	fams = append(fams, p31Family{
		name: "semantic-change",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				if err := tx.InsertItem(storetest.SemanticDirective(sess, "p1", "dep", tx.NextSeq(), "p1")); err != nil {
					return err
				}
				if err := tx.AppendLifecycleEvent(storetest.NewItemEvent(sess, "l1", tx.NextSeq(), "p1")); err != nil {
					return err
				}
				return tx.AppendLifecycleEvent(storetest.NewItemEvent(sess, "l2", tx.NextSeq(), "p1"))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertSemanticChange(domain.SemanticChange{
				SemanticMeta: storetest.Meta(sess, "c1", tx.NextSeq()), Target: domain.ItemGrantTarget(sess, "p1"),
				SourceAuthority: domain.AuthorityUser, Actor: storetest.NewPrincipal(sess, domain.AuthorityUser),
				Access: storetest.DirectiveBoundary(sess), Action: domain.ActionResolve,
				BeforeRevision: 1, AfterRevision: 2, BeforeStatus: "OPEN", AfterStatus: "RESOLVED",
				BeforeCurrentness: domain.ItemCurrent, AfterCurrentness: domain.ItemCurrent,
				AuditID: "l1", CauseID: "l2",
			})
		},
	})

	// MembershipWriter.
	fams = append(fams, p31Family{
		name: "logical-exchange",
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertLogicalExchange(storetest.NewExchange(sess, "x1", "task", "agent", 1, tx.NextSeq()))
		},
	})
	p31AdmissionWorld := func(t *testing.T, s store.Store, executing bool) {
		p31Update(t, s, func(tx store.Tx) error {
			sem := p31Sem(t, tx)
			in := storetest.NewItem(sess, "in", tx.NextSeq(), "input")
			if err := tx.InsertItem(in); err != nil {
				return err
			}
			if err := sem.InsertLogicalExchange(storetest.NewExchange(sess, "x1", "task", "agent", 1, tx.NextSeq())); err != nil {
				return err
			}
			cov, members := storetest.NewCoverage(t, sess, "gen", tx.NextSeq(), domain.CoverageGenerationInput, storetest.ContentRef(in))
			if err := sem.InsertCoverage(cov, members); err != nil {
				return err
			}
			_, err := sem.PutConversationMembership(domain.ConversationMembershipState{
				SemanticMeta: storetest.Meta(sess, "ms", tx.NextSeq()), ConversationID: domain.ConversationIDFor("task", "agent"),
				Revision: 1, LastOrdinal: 1}, 0)
			if err != nil {
				return err
			}
			if err := sem.InsertAdmissionManifest(domain.AdmissionManifest{
				SemanticMeta: storetest.Meta(sess, "adm", tx.NextSeq()), ConversationID: domain.ConversationIDFor("task", "agent"),
				ExchangeID: "x1", CallID: "call-1", Principal: storetest.AgentPrincipal(sess, "task", "agent"),
				TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "gen",
				MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion}); err != nil {
				return err
			}
			if !executing {
				return nil
			}
			x, err := sem.LogicalExchange("x1")
			if err != nil {
				return err
			}
			x.State = domain.ExchangeExecuting
			_, err = sem.PutLogicalExchange(x, 1)
			return err
		})
	}
	fams = append(fams, p31Family{
		name:  "logical-exchange-put",
		setup: func(t *testing.T, s store.Store) { p31AdmissionWorld(t, s, true) },
		write: func(t *testing.T, tx store.Tx) error {
			// A closing Put rides its acknowledgment's sequenced record, as
			// the acknowledgment path always writes it.
			sem := p31Sem(t, tx)
			if err := sem.InsertExchangeAcknowledgment(domain.ExchangeAcknowledgment{
				SemanticMeta: storetest.Meta(sess, "ack-put", tx.NextSeq()), ExchangeID: "x1", ManifestID: "adm",
				ConsumingCallID: "call-1", Actor: storetest.HarnessPrincipal(sess)}); err != nil {
				return err
			}
			x, err := sem.LogicalExchange("x1")
			if err != nil {
				return err
			}
			x.State, x.AcknowledgmentID = domain.ExchangeClosed, "ack-put"
			_, err = sem.PutLogicalExchange(x, x.Revision)
			return err
		},
	})
	var memberIn domain.ContextItem
	fams = append(fams, p31Family{
		name: "exchange-member",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				memberIn = storetest.NewItem(sess, "in", tx.NextSeq(), "input")
				if err := tx.InsertItem(memberIn); err != nil {
					return err
				}
				return p31Sem(t, tx).InsertLogicalExchange(storetest.NewExchange(sess, "x1", "task", "agent", 1, tx.NextSeq()))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertExchangeMember(domain.ExchangeMember{
				SemanticMeta: storetest.Meta(sess, "m1", tx.NextSeq()), ExchangeID: "x1", Position: 1,
				Role: domain.MemberInput, Source: storetest.ContentRef(memberIn)})
		},
	})
	fams = append(fams, p31Family{
		name:  "admission-manifest",
		setup: func(t *testing.T, s store.Store) { p31AdmissionWorld(t, s, false) },
		write: func(t *testing.T, tx store.Tx) error {
			// The world above already carries a manifest of its own; a second,
			// later-sequence manifest of the same exchange is a distinct record.
			return p31Sem(t, tx).InsertAdmissionManifest(domain.AdmissionManifest{
				SemanticMeta: storetest.Meta(sess, "adm2", tx.NextSeq()), ConversationID: domain.ConversationIDFor("task", "agent"),
				ExchangeID: "x1", CallID: "call-1", Principal: storetest.AgentPrincipal(sess, "task", "agent"),
				TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "gen",
				MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion})
		},
	})
	fams = append(fams, p31Family{
		name:  "exchange-acknowledgment",
		setup: func(t *testing.T, s store.Store) { p31AdmissionWorld(t, s, true) },
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertExchangeAcknowledgment(domain.ExchangeAcknowledgment{
				SemanticMeta: storetest.Meta(sess, "ack", tx.NextSeq()), ExchangeID: "x1", ManifestID: "adm",
				ConsumingCallID: "call-1", Actor: storetest.HarnessPrincipal(sess)})
		},
	})
	fams = append(fams, p31Family{
		name: "conversation-membership",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				sem := p31Sem(t, tx)
				if err := sem.InsertLogicalExchange(storetest.NewExchange(sess, "x1", "task", "agent", 1, tx.NextSeq())); err != nil {
					return err
				}
				return sem.InsertLogicalExchange(storetest.NewExchange(sess, "x2", "task", "agent", 2, tx.NextSeq()))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			_, err := p31Sem(t, tx).PutConversationMembership(domain.ConversationMembershipState{
				SemanticMeta: storetest.Meta(sess, "ms", tx.NextSeq()), ConversationID: domain.ConversationIDFor("task", "agent"),
				Revision: 1, LastOrdinal: 2}, 0)
			return err
		},
	})
	fams = append(fams, p31Family{
		name:  "checkpoint",
		setup: func(t *testing.T, s store.Store) { p31CheckpointWorld(t, s) },
		write: func(t *testing.T, tx store.Tx) error {
			seq := tx.NextSeq()
			return p31Sem(t, tx).InsertCheckpoint(domain.Checkpoint{
				SemanticMeta: storetest.Meta(sess, "c1", seq), ItemID: "ck1", ConversationID: domain.ConversationIDFor("task", "agent"),
				IssuingExchangeID: "x3", GenerationManifestID: "gen", SnapshotSeq: seq - 1, MembershipRevision: 1,
				CoveredFrontier: 2, SourceCoverageID: "src", CoveredExchangesID: "prefix", PolicyVersion: domain.Phase3PolicyVersion})
		},
	})
	fams = append(fams, p31Family{
		name: "owner-registration",
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertOwnerRegistration(domain.OwnerRegistration{
				SemanticMeta: storetest.Meta(sess, "wf-1", tx.NextSeq()), Kind: domain.OwnerWorkflow,
				OwnerID: "wf-1", SourceID: "evt", Actor: storetest.HarnessPrincipal(sess)})
		},
	})

	// ProofWriter.
	var boundObligation domain.ObligationVersion
	fams = append(fams, p31Family{
		name:  "obligation-declaration",
		setup: p31BoundWorld(&boundObligation, false, false),
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertObligationDeclaration(storetest.DeclarationOf(boundObligation, tx.NextSeq()))
		},
	})
	fams = append(fams, p31Family{
		name:  "applicability-proof-and-matcher-transition",
		setup: p31BoundWorld(&boundObligation, true, true),
		write: func(t *testing.T, tx store.Tx) error {
			// The facet commits a proof only with its satisfying transition
			// (and the transition only with its proof), so the two families
			// share one indivisible write.
			sem := p31Sem(t, tx)
			seq := tx.NextSeq()
			proof, deps := storetest.MatcherProof(t, boundObligation, "tr1", "obs1", "ev1", "evcov", seq)
			if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
				return err
			}
			tr, d := storetest.MatcherTransition(boundObligation, "tr1", seq, proof, "g-m")
			_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
			return err
		},
	})
	var attestObligation domain.ObligationVersion
	fams = append(fams, p31Family{
		name: "assertion-and-attestation-transition",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				if err := tx.InsertItem(storetest.SemanticDirective(sess, "src", "dep", tx.NextSeq(), "Keep the build green")); err != nil {
					return err
				}
				attestObligation = storetest.NewObligation(sess, "o1", 1, tx.NextSeq(), "src")
				attestObligation.Matcher = nil
				return tx.InsertObligationVersion(attestObligation)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			// An attestation's transition and its assertion record name each
			// other, so they commit as one write.
			sem := p31Sem(t, tx)
			seq := tx.NextSeq()
			user := storetest.NewPrincipal(sess, domain.AuthorityUser)
			tr := domain.ObligationTransition{ID: "tr1", SessionID: sess, ObligationID: "o1", Version: 1, Seq: seq,
				From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation,
				Actor: user, Cause: domain.CauseAssertion, AssertionMode: domain.AssertionAttestation,
				RequestID: "req-tr1", ReasonCode: domain.ReasonAuthorizedTransition}
			d := domain.TransitionDetail{SemanticMeta: storetest.Meta(sess, "td-tr1", seq), Target: storetest.Ref(attestObligation),
				TransitionID: "tr1", Cause: domain.CauseAssertion, AssertionID: "a1", RuleVersion: "rule/1"}
			a := domain.AssertionRecord{SemanticMeta: storetest.Meta(sess, "a1", seq), Target: storetest.Ref(attestObligation),
				Mode: domain.AssertionAttestation, Actor: user, TransitionID: "tr1", Access: attestObligation.Access}
			if _, err := sem.AppendSemanticObligationTransition(tr, d, 1); err != nil {
				return err
			}
			return sem.InsertAssertion(a)
		},
	})
	var materializationO domain.ObligationVersion
	fams = append(fams, p31Family{
		name: "materialization-exception",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				materializationO = storetest.NewObligation(sess, "o1", 1, tx.NextSeq(), "src")
				return tx.InsertObligationVersion(materializationO)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			_, err := p31Sem(t, tx).SetObligationMaterialization(storetest.Ref(materializationO), true, 1,
				storetest.NewLifecycleEvent(sess, "m1", tx.NextSeq(), domain.TargetObligation, "o1"))
			return err
		},
	})

	// ReceiptWriter.
	p31MutationReceipt := func(seq uint64) domain.MutationReceipt {
		principal := storetest.AgentPrincipal(sess, "task", "agent")
		args := []byte("canonical-args")
		id, err := domain.MutationReceiptKey(sess, domain.MutationTool, "req-1")
		if err != nil {
			panic(err)
		}
		h, err := domain.MutationRequestHash(principal, domain.MutationTool, "context_remember", args)
		if err != nil {
			panic(err)
		}
		return domain.MutationReceipt{SemanticMeta: storetest.Meta(sess, id, seq), Family: domain.MutationTool, RequestID: "req-1",
			Principal: principal, CanonicalMethod: "context_remember", CanonicalArguments: args,
			RequestHashVersion: domain.RequestHashV3, RequestHash: h, PolicyVersion: domain.Phase3PolicyVersion,
			Result: domain.MutationResult{Tool: &domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "k1", CanonicalItemID: "k1"}}}}
	}
	fams = append(fams, p31Family{
		name: "mutation-receipt",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				return tx.InsertItem(storetest.NewItem(sess, "k1", tx.NextSeq(), "remembered"))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertMutationReceipt(p31MutationReceipt(tx.NextSeq()))
		},
	})
	var mutationReceipt domain.MutationReceipt
	toolInvocation := domain.ToolInvocation{SessionID: sess, ConversationID: domain.ConversationIDFor("task", "agent"),
		CallID: "call-1", ToolCallID: "tc-1", ExchangeID: "x1", TurnID: "turn-1", Principal: storetest.AgentPrincipal(sess, "task", "agent")}
	fams = append(fams, p31Family{
		name: "tool-execution-receipt",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				if err := tx.InsertItem(storetest.NewItem(sess, "k1", tx.NextSeq(), "remembered")); err != nil {
					return err
				}
				mutationReceipt = p31MutationReceipt(tx.NextSeq())
				return p31Sem(t, tx).InsertMutationReceipt(mutationReceipt)
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			invID, err := toolInvocation.ID()
			if err != nil {
				return err
			}
			return p31Sem(t, tx).InsertToolExecutionReceipt(domain.ToolExecutionReceipt{
				SemanticMeta: storetest.Meta(sess, invID, tx.NextSeq()), Invocation: toolInvocation, Method: "context_remember",
				MutationReceiptID: mutationReceipt.ID, RequestHash: mutationReceipt.RequestHash,
				Result: domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "k1", CanonicalItemID: "k1"}}})
		},
	})
	fams = append(fams, p31Family{
		name: "gc-request",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				p31PutTask(t, tx)
				return p31Sem(t, tx).InsertGCRequest(storetest.NewGCRequest(sess, "gc1", tx.NextSeq()))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertGCRequest(storetest.NewGCRequest(sess, "gc2", tx.NextSeq()))
		},
	})
	fams = append(fams, p31Family{
		name: "collect-receipt-and-gc-result",
		setup: func(t *testing.T, s store.Store) {
			p31Update(t, s, func(tx store.Tx) error {
				sem := p31Sem(t, tx)
				p31PutTask(t, tx)
				if err := tx.InsertItem(storetest.NewItem(sess, "i1", tx.NextSeq(), "one")); err != nil {
					return err
				}
				return sem.InsertGCRequest(storetest.NewGCRequest(sess, "gc1", tx.NextSeq()))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			// A GC result exists only with its collect receipt, so the two
			// families share one write.
			sem := p31Sem(t, tx)
			seq := tx.NextSeq()
			ref := domain.ItemRevisionRef{ItemID: "i1", Version: 1}
			if err := sem.InsertCollectReceipt(domain.CollectReceipt{
				SemanticMeta: storetest.Meta(sess, "cr-gc1", seq), RequestID: "collect-gc1", GCRequestID: "gc1",
				PolicyVersion: domain.Phase3PolicyVersion, Principal: storetest.HarnessPrincipal(sess), SnapshotSeq: seq - 1,
				CandidateRefs: []domain.ItemRevisionRef{ref}, Decisions: []domain.GCDecision{{Target: ref, Code: domain.GCProtected}}}); err != nil {
				return err
			}
			return sem.InsertGCResult(domain.GCResult{SemanticMeta: storetest.Meta(sess, "gr-gc1", tx.NextSeq()),
				GCRequestID: "gc1", CollectReceiptID: "cr-gc1", Outcome: domain.GCCollected})
		},
	})

	// RetrievalWriter.
	p31RetrievalSource := func(t *testing.T, s store.Store) {
		p31Update(t, s, func(tx store.Tx) error {
			p31RetrievalSourceItem = storetest.NewItem(sess, "src", tx.NextSeq(), "historical fact")
			p31RetrievalSourceItem.Scope, p31RetrievalSourceItem.Access = domain.ScopeAgent, storetest.AgentBoundary(sess)
			return tx.InsertItem(p31RetrievalSourceItem)
		})
	}
	fams = append(fams, p31Family{
		name:  "retrieval-lease",
		setup: p31RetrievalSource,
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertRetrievalLease(storetest.NewLease(storetest.AgentOrigin(sess), "lease-1", tx.NextSeq(), storetest.ContentRef(p31RetrievalSourceItem)))
		},
	})
	fams = append(fams, p31Family{
		name:  "retrieval-projection-result-event",
		setup: func(t *testing.T, s store.Store) { p31RetrievalBundleWorld(t, s) },
		write: func(t *testing.T, tx store.Tx) error {
			// The projection names its result, the result its event, and the
			// event its result: the three families commit as one write over
			// the lease, projection item, and dependency coverage stored
			// beforehand.
			sem := p31Sem(t, tx)
			seq := tx.NextSeq()
			origin := storetest.AgentOrigin(sess)
			ref := storetest.ContentRef(p31RetrievalSourceItem)
			invID, err := origin.Invocation.ID()
			if err != nil {
				return err
			}
			projection := domain.ProjectionRecord{SemanticMeta: storetest.Meta(sess, "pr-1", seq), ItemID: "proj-1",
				Source: ref, LeaseID: "lease-1", RetrievalResultID: "res-1", DependencyCoverageID: "depcov-1",
				Origin: origin, Access: storetest.AgentBoundary(sess), DeliveryPolicyVersion: "delivery/1"}
			result := domain.RetrievalResult{SemanticMeta: storetest.Meta(sess, "res-1", seq), RequestID: "req-1",
				LeaseID: "lease-1", ProjectionID: "pr-1", RetrievalEventID: "rev-1", Origin: origin,
				Observed: domain.ObservedItemState{Source: ref, Version: 1, Currentness: domain.ItemUnkeyed,
					Generation: p31RetrievalSourceItem.Generation, Residency: p31RetrievalSourceItem.Residency,
					Authority: p31RetrievalSourceItem.Authority, Expiry: domain.ExpiryLive},
				Access: storetest.AgentBoundary(sess), PolicyVersion: domain.Phase3PolicyVersion}
			eventSrc := ref
			event := domain.RetrievalEvent{SemanticMeta: storetest.Meta(sess, "rev-1", seq), RequestID: "req-1",
				Principal: origin.Holder, TriggeringActor: origin.Holder, InvocationID: invID, ResultID: "res-1", Source: &eventSrc}
			if err := sem.InsertProjection(projection); err != nil {
				return err
			}
			if err := sem.InsertRetrievalResult(result); err != nil {
				return err
			}
			return sem.InsertRetrievalEvent(event)
		},
	})

	// ResourceWriter.
	fams = append(fams, p31Family{
		name: "resource-binding",
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertResourceBinding(storetest.NewResourceBinding(sess, "repo", tx.NextSeq()))
		},
	})
	var update1 domain.ResourceUpdate
	p31ResourceBound := func(t *testing.T, s store.Store, withUpdate bool, path string) {
		p31Update(t, s, func(tx store.Tx) error {
			sem := p31Sem(t, tx)
			if err := sem.InsertResourceBinding(storetest.NewResourceBinding(sess, "repo", tx.NextSeq())); err != nil {
				return err
			}
			if !withUpdate {
				return nil
			}
			var paths []string
			if path != "" {
				paths = append(paths, path)
			}
			update1 = storetest.NewResourceUpdate(sess, "u1", "repo", tx.NextSeq(), 0, p31fp, paths...)
			return sem.InsertResourceUpdate(update1)
		})
	}
	fams = append(fams, p31Family{
		name:  "resource-update",
		setup: func(t *testing.T, s store.Store) { p31ResourceBound(t, s, false, "") },
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertResourceUpdate(storetest.NewResourceUpdate(sess, "u1", "repo", tx.NextSeq(), 0, p31fp))
		},
	})
	fams = append(fams, p31Family{
		name:  "resource-state",
		setup: func(t *testing.T, s store.Store) { p31ResourceBound(t, s, true, "") },
		write: func(t *testing.T, tx store.Tx) error {
			_, err := p31Sem(t, tx).PutResourceState(storetest.StateAfter(update1, tx.NextSeq()), 0)
			return err
		},
	})
	fams = append(fams, p31Family{
		name:  "resource-path-state",
		setup: func(t *testing.T, s store.Store) { p31ResourceBound(t, s, true, "src/a.go") },
		write: func(t *testing.T, tx store.Tx) error {
			_, err := p31Sem(t, tx).PutResourcePathState(domain.ResourcePathState{
				SemanticMeta: storetest.Meta(sess, "ps-a", tx.NextSeq()),
				Locator:      domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "src/a.go"},
				ContentHash:  domain.HashBytes([]byte("v2")), ResourceUpdateID: update1.ID,
				ResourceRevision: update1.ResultingAuthoritativeRevision, Revision: 1, Freshness: domain.ResourceKnown}, 0)
			return err
		},
	})
	fams = append(fams, p31Family{
		name:  "workspace-binding",
		setup: func(t *testing.T, s store.Store) { p31ResourceBound(t, s, false, "") },
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertWorkspaceBinding(storetest.NewWorkspaceBinding(sess, "wb", "repo", 1, tx.NextSeq()))
		},
	})
	var observationRun domain.ObservationRun
	p31WorkspaceReady := func(t *testing.T, s store.Store) {
		p31Update(t, s, func(tx store.Tx) error {
			sem := p31Sem(t, tx)
			p31PutTask(t, tx)
			if err := sem.InsertResourceBinding(storetest.NewResourceBinding(sess, "repo", tx.NextSeq())); err != nil {
				return err
			}
			return sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding(sess, "wb", "repo", 1, tx.NextSeq()))
		})
	}
	fams = append(fams, p31Family{
		name:  "observation-run",
		setup: p31WorkspaceReady,
		write: func(t *testing.T, tx store.Tx) error {
			observationRun = storetest.NewObservationRun(t, sess, "run1", "repo", "wb", tx.NextSeq())
			return p31Sem(t, tx).InsertObservationRun(observationRun)
		},
	})
	fams = append(fams, p31Family{
		name: "observation",
		setup: func(t *testing.T, s store.Store) {
			p31WorkspaceReady(t, s)
			p31Update(t, s, func(tx store.Tx) error {
				run := storetest.NewObservationRun(t, sess, "run1", "repo", "wb", tx.NextSeq())
				observationRun = run
				if err := p31Sem(t, tx).InsertObservationRun(run); err != nil {
					return err
				}
				return tx.InsertItem(storetest.ProducedEvidence(sess, "ev1", tx.NextSeq(), run.ExecutionID))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			return p31Sem(t, tx).InsertObservation(storetest.NewObservation(observationRun, "obs1", "ev1", tx.NextSeq(), p31fp))
		},
	})
	fams = append(fams, p31Family{
		name: "subject-state",
		setup: func(t *testing.T, s store.Store) {
			p31WorkspaceReady(t, s)
			p31Update(t, s, func(tx store.Tx) error {
				sem := p31Sem(t, tx)
				run := storetest.NewObservationRun(t, sess, "run1", "repo", "wb", tx.NextSeq())
				if err := sem.InsertObservationRun(run); err != nil {
					return err
				}
				observationRun = run
				if err := tx.InsertItem(storetest.ProducedEvidence(sess, "ev1", tx.NextSeq(), run.ExecutionID)); err != nil {
					return err
				}
				return sem.InsertObservation(storetest.NewObservation(run, "obs1", "ev1", tx.NextSeq(), p31fp))
			})
		},
		write: func(t *testing.T, tx store.Tx) error {
			_, err := p31Sem(t, tx).PutSubjectState(domain.SubjectState{
				SemanticMeta: storetest.Meta(sess, "ss", tx.NextSeq()), SubjectKey: observationRun.SubjectKey,
				TaskID: "task", CurrentItemID: "ev1", ObservationID: "obs1", Access: observationRun.Access,
				AcceptedOrdinal: observationRun.Ordinal, Revision: 1, Applicability: domain.ApplicabilityCurrent}, 0, "obs1")
			return err
		},
	})

	return fams
}

// p31CheckpointWorld stores the checkpoint family's prerequisites: three
// exchanges, a membership state, the generation-input source coverage and
// manifest of the issuing round, the closed-prefix exchange coverage, and
// the CHECKPOINT item itself.
func p31CheckpointWorld(t *testing.T, s store.Store) {
	t.Helper()
	p31Update(t, s, func(tx store.Tx) error {
		sem := p31Sem(t, tx)
		conv := domain.ConversationIDFor("task", "agent")
		in := storetest.NewItem(sess, "in", tx.NextSeq(), "input")
		if err := tx.InsertItem(in); err != nil {
			return err
		}
		for i, id := range []string{"x1", "x2", "x3"} {
			if err := sem.InsertLogicalExchange(storetest.NewExchange(sess, id, "task", "agent", uint64(i+1), tx.NextSeq())); err != nil {
				return err
			}
		}
		if _, err := sem.PutConversationMembership(domain.ConversationMembershipState{
			SemanticMeta: storetest.Meta(sess, "ms", tx.NextSeq()), ConversationID: conv, Revision: 1, LastOrdinal: 3}, 0); err != nil {
			return err
		}
		src, srcMembers := storetest.NewCoverage(t, sess, "src", tx.NextSeq(), domain.CoverageGenerationInput, storetest.ContentRef(in))
		if err := sem.InsertCoverage(src, srcMembers); err != nil {
			return err
		}
		if err := sem.InsertAdmissionManifest(domain.AdmissionManifest{
			SemanticMeta: storetest.Meta(sess, "gen", tx.NextSeq()), ConversationID: conv, ExchangeID: "x3", CallID: "call-3",
			Principal: storetest.AgentPrincipal(sess, "task", "agent"), TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput,
			CoverageID: "src", MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion}); err != nil {
			return err
		}
		seq := tx.NextSeq()
		prefix, prefixMembers := p31SignCoverage(t, domain.CoverageRecord{
			SemanticMeta: storetest.Meta(sess, "prefix", seq), Purpose: domain.CoverageExchangeReplacement,
			Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}, ConversationID: conv,
			MembershipRevision: 1, ClosedFrontier: 2,
		}, []domain.CoverageMember{
			{SemanticMeta: storetest.Meta(sess, "", seq), CoverageID: "prefix", ExchangeID: "x1"},
			{SemanticMeta: storetest.Meta(sess, "", seq), CoverageID: "prefix", ExchangeID: "x2"},
		})
		if err := sem.InsertCoverage(prefix, prefixMembers); err != nil {
			return err
		}
		ck := storetest.NewItem(sess, "ck1", tx.NextSeq(), "summary ck1")
		ck.Kind, ck.Role = domain.KindSummary, domain.RoleCheckpoint
		ck.Authority, ck.Scope = domain.AuthorityAgent, domain.ScopeAgent
		ck.Access = storetest.AgentBoundary(sess)
		return tx.InsertItem(ck)
	})
}

// p31RetrievalSourceItem carries the retrieval source item from a setup to
// its family's write.
var p31RetrievalSourceItem domain.ContextItem

// p31RetrievalBundleWorld stores the retrieval bundle's prerequisites: the
// source item, the lease, the TOOL projection item, and the lease-dependency
// coverage, leaving the projection, result, and event as the family's write.
func p31RetrievalBundleWorld(t *testing.T, s store.Store) {
	t.Helper()
	p31Update(t, s, func(tx store.Tx) error {
		p31RetrievalSourceItem = storetest.NewItem(sess, "src", tx.NextSeq(), "historical fact")
		p31RetrievalSourceItem.Scope, p31RetrievalSourceItem.Access = domain.ScopeAgent, storetest.AgentBoundary(sess)
		if err := tx.InsertItem(p31RetrievalSourceItem); err != nil {
			return err
		}
		return nil
	})
	p31Update(t, s, func(tx store.Tx) error {
		sem := p31Sem(t, tx)
		if err := sem.InsertRetrievalLease(storetest.NewLease(storetest.AgentOrigin(sess), "lease-1", tx.NextSeq(), storetest.ContentRef(p31RetrievalSourceItem))); err != nil {
			return err
		}
		proj := storetest.NewItem(sess, "proj-1", tx.NextSeq(), "retrieved: 1")
		proj.Authority, proj.Role, proj.Scope, proj.Access = domain.AuthorityTool, domain.RoleProjection, domain.ScopeAgent, storetest.AgentBoundary(sess)
		if err := tx.InsertItem(proj); err != nil {
			return err
		}
		seq := tx.NextSeq()
		ref := storetest.ContentRef(p31RetrievalSourceItem)
		cov, members := p31SignCoverage(t, domain.CoverageRecord{
			SemanticMeta: storetest.Meta(sess, "depcov-1", seq), Purpose: domain.CoverageLeaseDependency,
			Access: storetest.AgentBoundary(sess),
		}, []domain.CoverageMember{{SemanticMeta: storetest.Meta(sess, "", seq), CoverageID: "depcov-1", Source: &ref, LeaseID: "lease-1"}})
		return sem.InsertCoverage(cov, members)
	})
}

// TestP3_1_PrepareMarkSentStaleAfterEverySemanticRecordFamily: every Phase 3
// semantic record family's committed write consumes a sequence outside the
// TargetCall lifecycle, so a reservation made before it is stale afterwards:
// MarkSent fails ErrVersionConflict leaving the call PREPARED with zero
// attempts, and even an identical Prepare repeat fails stale instead of
// returning the held reservation. The control row proves MarkSent succeeds
// when no semantic record intervenes, on both stores.
func TestP3_1_PrepareMarkSentStaleAfterEverySemanticRecordFamily(t *testing.T) {
	for _, fam := range p31Families() {
		for _, kind := range []string{"memory", "sqlite"} {
			t.Run(fam.name+"/"+kind, func(t *testing.T) {
				var s store.Store
				if kind == "memory" {
					ms := memory.New()
					t.Cleanup(func() { ms.Close() })
					s = ms
				} else {
					db := sqlitetest.Open(t)
					t.Cleanup(func() { db.Close() })
					s = db
				}
				l := newLedger(s)
				if fam.setup != nil {
					fam.setup(t, s)
				}
				base := lastSeq(t, s)
				req := request(agentA, "p31-body", 1, base, 0)
				call, err := l.Prepare(ctx, req)
				must(t, err)
				if fam.control {
					// Nothing intervened: dispatch still succeeds.
					if _, err := l.MarkSent(ctx, harness, call.CallID, "prov-1"); err != nil {
						t.Fatalf("control MarkSent: %v", err)
					}
					wantState(t, s, call.CallID, domain.CallSent)
					return
				}
				p31Update(t, s, func(tx store.Tx) error { return fam.write(t, tx) })
				if after := lastSeq(t, s); after <= base+1 {
					t.Fatalf("family write consumed no sequence: lastSeq %d, reservation ended at %d", after, base+1)
				}
				_, err = l.MarkSent(ctx, harness, call.CallID, "prov-1")
				wantErr(t, err, domain.ErrVersionConflict)
				wantState(t, s, call.CallID, domain.CallPrepared)
				if n := len(attempts(t, s, call.CallID)); n != 0 {
					t.Errorf("stale MarkSent left %d attempts", n)
				}
				_, err = l.Prepare(ctx, req)
				wantErr(t, err, domain.ErrVersionConflict)
			})
		}
	}
}
