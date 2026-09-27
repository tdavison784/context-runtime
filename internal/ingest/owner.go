package ingest

import (
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Owner registration (P3-32/C-15, SPEC-1.7). A WORKFLOW or AGENT owner
// becomes ACTIVE for the session through its first trusted association:
// the first item an authenticated principal creates at that scope. The
// registration is immutable, explicit state, independent of item
// visibility, and ending the owner's tasks never ends it. Without one, a
// broad-scope item's lifetime is UNKNOWN (never ordinarily selectable,
// never archived).

// ownerRegistrationIDDomain derives an owner registration's record ID from
// its session, kind and owner ID, the store's uniqueness key.
const ownerRegistrationIDDomain = "context-runtime/ingest/owner-registration-id/v1"

// registerOwner records the owner of it, a new item about to be inserted,
// if it is the first WORKFLOW/AGENT-scoped association of that owner. The
// owner ID is the authenticated principal's, never content's, and must be
// the item's own boundary owner; only an item of a principal-attested
// authority (SYSTEM, HARNESS, USER, AGENT) associates, never TOOL output
// or retrieved content. The registration is written by the trusted
// ingestion service (HARNESS) for the principal, before the item, in the
// item's transaction. An owner ID a registration cannot hold leaves the
// lifetime UNKNOWN (fail closed) rather than rejecting the event.
func (r *run) registerOwner(it domain.ContextItem) error {
	if r.pol == nil {
		return nil
	}
	var kind domain.OwnerKind
	var id string
	switch it.Scope {
	case domain.ScopeWorkflow:
		kind, id = domain.OwnerWorkflow, r.p.WorkflowID
		if it.Access.WorkflowID != id {
			return nil
		}
	case domain.ScopeAgent:
		kind, id = domain.OwnerAgent, r.p.AgentID
		if it.Access.AgentID != id {
			return nil
		}
	default:
		return nil
	}
	switch it.Authority {
	case domain.AuthoritySystem, domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent:
	default:
		return nil
	}
	if id == "" || it.Access.SessionID != r.p.SessionID {
		return nil
	}
	sem, err := store.Semantic(r.tx)
	if err != nil {
		return err
	}
	if _, err := sem.OwnerRegistration(kind, id); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	o := domain.OwnerRegistration{
		SemanticMeta: domain.SemanticMeta{ID: ownerRegistrationID(r.p.SessionID, kind, id), SessionID: r.p.SessionID, SchemaVersion: domain.SemanticSchemaV1},
		Kind:         kind, OwnerID: id, WorkflowID: r.p.WorkflowID, SourceID: r.occurrence,
		Actor: domain.Principal{SessionID: r.p.SessionID, WorkflowID: r.p.WorkflowID, Authority: domain.AuthorityHarness},
	}
	o.Seq = 1 // validated before a sequence is allocated
	if o.Validate() != nil {
		return nil
	}
	o.Seq = r.tx.NextSeq()
	return sem.InsertOwnerRegistration(o)
}

func ownerRegistrationID(session string, kind domain.OwnerKind, ownerID string) string {
	c := domain.NewCanonicalEncoder(ownerRegistrationIDDomain).String(session).String(string(kind)).String(ownerID)
	return "own_" + strings.TrimPrefix(c.Hash(), "sha256:")
}
