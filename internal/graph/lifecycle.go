package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrLifecycleTargetMismatch reports a lifecycle command whose resolved,
// accessible target is not of the kind or state its action requires:
// Resolve needs an OPEN goal and Unpin a PINNED item (FR-DIR-005). The
// target is already visible to the source actor, so this discloses nothing
// beyond what it could read.
var ErrLifecycleTargetMismatch = errors.New("graph: lifecycle target is not of the kind or state its action requires")

// LifecycleAuthorization is the read-only outcome of
// AuthorizeLifecycleCommand. It describes a parsed command that WOULD be
// authorized; it is never a lifecycle transition, and nothing was changed
// (D1: Phase 2 commands are PARSED_NOT_EXECUTED). A resolved ID is not a
// capability: whoever executes the command later must revalidate access,
// source authorization, the target's version and currentness, and grants
// in the executing transaction.
type LifecycleAuthorization struct {
	// Command is the input with ResolvedItemID set to the unique target.
	Command domain.LifecycleCommand
	// SourceActor is the principal the command was authorized as: the
	// caller's authenticated ownership IDs with the span's authority (D15).
	SourceActor domain.Principal
	// TargetVersion is the target's version when it was checked.
	TargetVersion uint64
	// GrantID names the action-specific grant that authorized the command
	// (FR-AUTH-002), or is empty when the source actor's own authority did.
	GrantID string
}

// AuthorizeLifecycleCommand resolves and authorizes a parsed Resolve or
// Unpin command for the SOURCE actor of the span that carried it (D1, D15,
// R7) without executing it. caller is the authenticated ingestion
// principal; the command's Authority is its span's authority. It never
// writes, so it takes a store.ReadTx; run it inside the ingestion
// transaction so it sees the state at the command's position in event
// order.
//
//   - The source actor is domain.SourceActor(caller, cmd.Authority): a
//     span that outranks its caller fails with
//     domain.ErrInvalidAuthorityPromotion.
//   - The target is resolved with ResolveLifecycleTarget as the source
//     actor (DIRECTIVE namespace only, R6). Unknown and inaccessible
//     targets both fail with the identical bare domain.ErrNotFound;
//     several accessible current versions fail with ErrAmbiguousDirective.
//     Callers report these as diagnostics; they do not abort the event.
//   - The target must be of the action's kind and state
//     (ErrLifecycleTargetMismatch), which is also a diagnostic, not an
//     abort (R14).
//   - The action is authorized with domain.AuthorizeMutation for the
//     source actor, against the session's grants, at the next sequence
//     number. Failure is domain.ErrInvalidAuthorityPromotion, which aborts
//     the event (R7, FR-AUTH-001). The stronger caller's authority is never
//     consulted, so a USER span carried by a SYSTEM caller cannot borrow
//     SYSTEM authority (confused deputy).
func AuthorizeLifecycleCommand(tx store.ReadTx, caller domain.Principal, taskID string, cmd domain.LifecycleCommand) (LifecycleAuthorization, error) {
	if err := cmd.Validate(); err != nil {
		return LifecycleAuthorization{}, err
	}
	actor, err := domain.SourceActor(caller, cmd.Authority)
	if err != nil {
		return LifecycleAuthorization{}, err
	}

	targetID, err := ResolveLifecycleTarget(tx, actor, taskID, cmd.TargetID)
	if err != nil {
		return LifecycleAuthorization{}, err
	}
	target, err := loadAccessible(tx, actor, targetID)
	if err != nil {
		return LifecycleAuthorization{}, err
	}

	var action domain.Action
	switch cmd.Action {
	case domain.LifecycleResolve:
		if target.Kind != domain.KindGoal || target.GoalStatus == nil || *target.GoalStatus != domain.GoalOpen {
			return LifecycleAuthorization{}, ErrLifecycleTargetMismatch
		}
		action = domain.ActionResolve
	case domain.LifecycleUnpin:
		if target.Generation != domain.GenerationPinned {
			return LifecycleAuthorization{}, ErrLifecycleTargetMismatch
		}
		action = domain.ActionUnpin
	default:
		return LifecycleAuthorization{}, domain.ErrInvalidRecord
	}

	grants, err := tx.Grants()
	if err != nil {
		return LifecycleAuthorization{}, err
	}
	auth, err := domain.AuthorizeMutation(domain.MutationRequest{
		Actor:   actor,
		Action:  action,
		Targets: []domain.MutationTarget{{ID: target.ID, Authority: target.Authority, Access: target.Access}},
		Grants:  grants,
		Seq:     tx.LastSeq() + 1,
	})
	if err != nil {
		return LifecycleAuthorization{}, err
	}

	cmd.ResolvedItemID = target.ID
	return LifecycleAuthorization{
		Command:       cmd,
		SourceActor:   actor,
		TargetVersion: target.Version,
		GrantID:       auth.GrantIDs[target.ID],
	}, nil
}
