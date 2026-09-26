package lifecycle

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestCompletionOwnershipAndState(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	task := storetest.NewTask("s", p.TaskID)
	for _, tc := range []struct {
		name      string
		principal domain.Principal
		task      domain.TaskState
		want      error
	}{
		{"owner", p, task, nil},
		{"foreign workflow", func() domain.Principal { q := p; q.WorkflowID = "other"; return q }(), task, domain.ErrNotFound},
		{"empty task", func() domain.Principal { q := p; q.TaskID = ""; return q }(), task, domain.ErrNotFound},
		{"session role not wildcard", func() domain.Principal { q := p; q.Authority = domain.AuthoritySystem; q.TaskID = "other"; return q }(), task, domain.ErrNotFound},
		{"agent", func() domain.Principal { q := p; q.Authority = domain.AuthorityAgent; return q }(), task, domain.ErrInvalidAuthorityPromotion},
		{"completed new request", p, func() domain.TaskState { q := task; q.Status = domain.TaskCompleted; q.CompletedSeq = 8; return q }(), domain.ErrInvalidTransition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := completionOwner(tc.principal, tc.task); err != tc.want {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
