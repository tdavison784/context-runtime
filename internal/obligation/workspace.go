package obligation

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// BindWorkspaceTx records one immutable version of a workspace binding from a
// source, task, or conversation to a registered resource (P3-20, C-11). There
// is no session-wide current workspace: a binding applies only to its own
// context. The binder is the authenticated SYSTEM/HARNESS actor, who must be
// able to see both the context and the resource registration.
func (s *Service) BindWorkspaceTx(tx store.Tx, actor domain.Principal, in domain.WorkspaceBindingIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationWorkspaceBind, in.RequestID, "BindWorkspace", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(sem, actor, req); ok || err != nil {
		return res, err
	}
	seq = allocate(tx, seq)
	if !trustedControl(actor) {
		return domain.MutationResult{}, domain.ErrInvalidAuthorityPromotion
	}
	if !opaqueSpec(in.EnvironmentSpec, false) || !opaqueSpec(in.SuiteSpec, true) || !opaqueSpec(in.CoverageSpec, true) {
		return domain.MutationResult{}, domain.ErrInvalidRecord
	}
	res, err := sem.ResourceBinding(in.ResourceID)
	if err != nil || !res.Access.Permits(actor) {
		return domain.MutationResult{}, notFound(err)
	}
	if err := checkWorkspaceContext(tx, actor, in.Context); err != nil {
		return domain.MutationResult{}, err
	}
	w := &writes{tx: tx}
	w.start()
	b := domain.WorkspaceBinding{
		Context:         in.Context,
		SemanticMeta:    domain.SemanticMeta{ID: in.BindingID, SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Version:         in.Version,
		ResourceID:      in.ResourceID,
		SourceItemID:    in.SourceItemID,
		TaskID:          in.TaskID,
		ConversationID:  in.ConversationID,
		BaseDir:         in.BaseDir,
		EnvironmentSpec: in.EnvironmentSpec,
		SuiteSpec:       in.SuiteSpec,
		CoverageSpec:    in.CoverageSpec,
		Access:          in.Access,
		Reporter:        actor,
	}
	if err := sem.InsertWorkspaceBinding(b); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "WORKSPACE_BINDING", IDs: []string{in.BindingID}}}
	if err := s.recordReceipt(sem, actor, req, seq, result); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	return result, nil
}

// trustedControl reports whether p may perform control-plane operations
// (workspace, resource, run, and observation reporting).
func trustedControl(p domain.Principal) bool {
	return p.Authority == domain.AuthoritySystem || p.Authority == domain.AuthorityHarness
}

// opaqueSpec reports whether s is an opaque specification name: a closed
// token (FR-DIR-006 value grammar) or a content hash. Environment variable
// assignments, paths, and command text are rejected so reporter-supplied
// values never become trusted template content (P3-21).
func opaqueSpec(s string, optional bool) bool {
	if s == "" {
		return optional
	}
	return len(s) <= 128 && domain.ValidAttributeValue(s) || domain.ValidHash(s)
}

// notFound maps a failed or inaccessible lookup to ErrNotFound, keeping
// operational errors that are not absence (P3-24 fixed errors).
func notFound(err error) error {
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func checkWorkspaceContext(tx store.Tx, actor domain.Principal, c domain.WorkspaceSourceContext) error {
	switch c.Kind {
	case domain.WorkspaceSource:
		it, err := tx.Item(c.ID)
		if err != nil || !it.Access.Permits(actor) {
			return notFound(err)
		}
	case domain.WorkspaceTask:
		if actor.TaskID != "" && actor.TaskID != c.ID {
			return domain.ErrNotFound
		}
		if _, err := tx.Task(c.ID); err != nil {
			return notFound(err)
		}
	case domain.WorkspaceConversation:
		if _, err := tx.Conversation(c.ID); err != nil {
			return notFound(err)
		}
	default:
		return domain.ErrInvalidRecord
	}
	return nil
}

// resolveWorkspace finds the one trusted workspace binding that applies to a
// source (P3-12/20). Source-context bindings shadow task-context ones. A
// binding applies only if its reporter's authority is at least the source's
// (Q-3: a HARNESS binding cannot choose the suite that satisfies a SYSTEM
// requirement) and its boundary covers the source's. Only each binding ID's
// latest version is considered, and only while that version is in the
// context. More than one applicable binding is ambiguous; candidates that
// exist but do not apply yield BINDING_AUTHORITY.
func (s *Service) resolveWorkspace(r store.SemanticReader, work *budget, source domain.ContextItem) (Workspace, error) {
	contexts := [][3]string{{source.ID, "", ""}}
	if source.TaskID != "" {
		contexts = append(contexts, [3]string{"", source.TaskID, ""})
	}
	for _, c := range contexts {
		// Only each binding's current version is read, so its version
		// history never counts against the work bound (H2).
		var latest []domain.WorkspaceBinding
		err := s.eachPage(work, func(p store.Page) (int, store.Cursor, bool, error) {
			pg, err := r.CurrentWorkspaceBindingsByContext(c[0], c[1], c[2], p)
			if err != nil {
				return 0, store.Cursor{}, false, err
			}
			latest = append(latest, pg.Records...)
			return len(pg.Records), pg.Next, pg.More, nil
		})
		if err != nil {
			return Workspace{}, err
		}
		if len(latest) == 0 {
			continue
		}
		var applicable []domain.WorkspaceBinding
		for _, b := range latest {
			if b.Reporter.Authority.AtLeast(source.Authority) && source.Access.Within(b.Access) {
				applicable = append(applicable, b)
			}
		}
		switch len(applicable) {
		case 0:
			return Workspace{Reason: domain.ReasonBindingAuthority}, nil
		case 1:
			return Workspace{Binding: &applicable[0]}, nil
		}
		return Workspace{Reason: domain.ReasonBindingAmbiguous}, nil
	}
	return Workspace{}, nil
}

// budget is a transaction's hard bound on correctness-critical read work
// (P3-39). Exhausting it fails the operation; required fan-out never stops
// early with a partial answer.
type budget struct{ left int }

func (s *Service) newBudget() *budget { return &budget{left: s.policy.MaxTransactionWork} }

func (b *budget) spend(n int) error {
	if n < 0 || n > b.left {
		b.left = 0
		return domain.ErrResourceLimit
	}
	b.left -= n
	return nil
}

// eachPage exhausts a paged read under the policy's page size, charging each
// page and its records to b.
func (s *Service) eachPage(b *budget, next func(store.Page) (n int, after store.Cursor, more bool, err error)) error {
	return s.eachPageFrom(b, store.Cursor{}, next)
}

// eachPageFrom is eachPage starting strictly after cursor from.
func (s *Service) eachPageFrom(b *budget, from store.Cursor, next func(store.Page) (n int, after store.Cursor, more bool, err error)) error {
	p := store.Page{After: from, Limit: s.policy.MaxPageSize}
	for {
		n, after, more, err := next(p)
		if err != nil {
			return err
		}
		if err := b.spend(n + 1); err != nil {
			return err
		}
		if !more {
			return nil
		}
		p.After = after
	}
}
