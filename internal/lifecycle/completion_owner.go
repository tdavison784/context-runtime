package lifecycle

import "github.com/tdavison784/context-runtime/internal/domain"

func completionOwner(p domain.Principal, task domain.TaskState) error {
	if !p.Authority.CanHoldLifecycleAuthority() {
		return domain.ErrInvalidAuthorityPromotion
	}
	if p.TaskID == "" || p.SessionID != task.SessionID || p.TaskID != task.TaskID || p.WorkflowID != task.WorkflowID {
		return domain.ErrNotFound
	}
	if task.Validate() != nil {
		return domain.ErrIntegrity
	}
	if task.Status != domain.TaskActive {
		return domain.ErrInvalidTransition
	}
	return nil
}
