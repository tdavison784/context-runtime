package store

import "github.com/tdavison784/context-runtime/internal/domain"

type ProofReader interface {
	ExactObligation(domain.ObligationRef) (domain.ObligationVersion, error)
	ObligationDeclaration(domain.ObligationRef) (domain.ObligationDeclaration, error)
	ApplicabilityProof(id string) (domain.ApplicabilityProof, error)
	Assertion(id string) (domain.AssertionRecord, error)
	TransitionDetail(transitionID string) (domain.TransitionDetail, error)
	ProofDependencies(proofID string, page Page) (ResultPage[domain.ProofDependency], error)
	CurrentBoundObligationsBySubject(subjectKey string, page Page) (ResultPage[domain.ObligationVersion], error)
	// Empty pathKey means ALL potentially intersecting dependencies. Results
	// must include workspace/unknown-path dependencies as well as exact paths.
	CurrentProofsByDependency(resourceID, pathKey string, page Page) (ResultPage[domain.ApplicabilityProof], error)
	// No access filter: completion must inspect all declared TURN/TASK owners.
	ObligationsByTaskOwner(taskID string, page Page) (ResultPage[domain.ObligationVersion], error)
	TransitionsByVersion(target domain.ObligationRef, page Page) (ResultPage[domain.ObligationTransition], error)
	Satisfies(viewer domain.Principal, target domain.ObligationRef, currentOnly bool, page Page) (ResultPage[domain.SatisfiesRelation], error)
}
type ProofWriter interface {
	InsertObligationDeclaration(domain.ObligationDeclaration) error
	// Proof/dependencies are indivisible; satisfying transition references may
	// be fulfilled later in this transaction, but MUST exist before commit.
	InsertApplicabilityProof(domain.ApplicabilityProof, []domain.ProofDependency) error
	InsertAssertion(domain.AssertionRecord) error
	// Atomically append history/detail and CAS status/evidence/proof caches.
	// A proof-refresh pair may call this twice with successive revisions.
	AppendSemanticObligationTransition(domain.ObligationTransition, domain.TransitionDetail, uint64) (domain.ObligationVersion, error)
	SetObligationMaterialization(target domain.ObligationRef, disabled bool, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error)
}
