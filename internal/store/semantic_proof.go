package store

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
)

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
	// LiveProofsByPath pages the live proofs with a CURRENT_PATH dependency
	// on resourceID at path or below it: each such dependency is indexed
	// under every ancestor key of its path, so a changed file or directory
	// is one exact key (DUR-3.1). FIXED_CONTENT dependencies are in no live
	// index.
	LiveProofsByPath(resourceID, path string, page Page) (ResultPage[domain.ApplicabilityProof], error)
	// LiveWorkspaceProofs pages the live proofs with a WORKSPACE dependency
	// on resourceID, read only when the workspace fingerprint changes.
	LiveWorkspaceProofs(resourceID string, page Page) (ResultPage[domain.ApplicabilityProof], error)
	// LiveProofDependents is the write-time count of live proofs with a
	// CURRENT_PATH or WORKSPACE dependency on resourceID (0 when none).
	LiveProofDependents(resourceID string) (uint64, error)
	// No access filter: completion must inspect all declared TURN/TASK owners.
	ObligationsByTaskOwner(taskID string, page Page) (ResultPage[domain.ObligationVersion], error)
	// ObligationTransition is one transition by its exact ID (H2): a
	// proof's satisfying transition without paging the version's history.
	ObligationTransition(id string) (domain.ObligationTransition, error)
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

// ValidateSatisfactionBacking enforces INV-16's shape (DUR-1.9): a SATISFIED
// transition carries exactly the backing its mode requires. A matcher
// satisfies through its matcher proof alone; any other satisfaction names
// its assertion, which commit checks against the transition's mode and
// proof. A nonpositive transition carries neither.
func ValidateSatisfactionBacking(tr domain.ObligationTransition, d domain.TransitionDetail) error {
	switch {
	case tr.To != domain.ObligationSatisfied:
		if d.AssertionID != "" {
			return fmt.Errorf("transition %s: a nonpositive transition names an assertion: %w", tr.ID, domain.ErrInvalidRecord)
		}
	case tr.Matcher != nil:
		if tr.ProofID == "" || d.AssertionID != "" {
			return fmt.Errorf("transition %s: a matcher satisfies through its proof alone: %w", tr.ID, domain.ErrInvalidRecord)
		}
	case d.AssertionID == "":
		return fmt.Errorf("transition %s: SATISFIED without its assertion: %w", tr.ID, domain.ErrInvalidRecord)
	}
	return nil
}
