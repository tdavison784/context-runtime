package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func command(action domain.LifecycleAction, target string, spanAuthority domain.Authority) domain.LifecycleCommand {
	return domain.LifecycleCommand{Action: action, TargetID: target, Authority: spanAuthority, Range: domain.ByteRange{Start: 0, End: 10}}
}

// lifecycleFixture files, in task "task": a SYSTEM goal "sys-goal", a USER
// goal "user-goal", a USER pin "user-pin", and an agent-b private goal
// "hidden-goal".
func lifecycleFixture(t *testing.T, s store.Store, sess string) {
	t.Helper()
	update(t, s, sess, func(tx store.Tx) error {
		sysGoal := goalLike(sess, "sys-goal-1", "sys-goal", tx.NextSeq(), "System goal")
		sysGoal.Authority = domain.AuthoritySystem
		userGoal := goalLike(sess, "user-goal-1", "user-goal", tx.NextSeq(), "User goal")
		pin := storetest.NewDirective(sess, "user-pin-1", "user-pin", tx.NextSeq(), "Pinned")
		hidden := goalLike(sess, "hidden-goal-1", "hidden-goal", tx.NextSeq(), "Private goal")
		hidden.Scope = domain.ScopeAgent
		hidden.AgentID = "agent-b"
		hidden.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, AgentID: "agent-b"}
		mustInsert(t, tx, sysGoal, userGoal, pin, hidden)
		mustFile(t, tx, sysGoal, userGoal, pin, hidden)
		return nil
	})
}

// TestD1_AuthorizeLifecycleCommand_SourceActor is D1/D15/R7: a parsed
// Resolve/Unpin is authorized for the SOURCE actor (the caller's
// authenticated ownership with the span's authority), never the stronger
// ingestion caller, and nothing is executed.
func TestD1_AuthorizeLifecycleCommand_SourceActor(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d1"
		lifecycleFixture(t, s, sess)
		system := principal(sess, domain.AuthoritySystem)
		user := principal(sess, domain.AuthorityUser)

		var before uint64
		view(t, s, sess, func(tx store.ReadTx) error {
			before = tx.LastSeq()

			got, err := AuthorizeLifecycleCommand(tx, user, "task", command(domain.LifecycleResolve, "user-goal", domain.AuthorityUser))
			if err != nil {
				t.Errorf("USER Resolve of USER goal: %v", err)
			}
			if got.Command.ResolvedItemID != "user-goal-1" || got.GrantID != "" || got.SourceActor.Authority != domain.AuthorityUser {
				t.Errorf("result = %+v", got)
			}

			// Confused deputy: a USER span carried by a SYSTEM caller
			// cannot resolve a SYSTEM goal.
			_, err = AuthorizeLifecycleCommand(tx, system, "task", command(domain.LifecycleResolve, "sys-goal", domain.AuthorityUser))
			if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Errorf("USER span via SYSTEM caller: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			// The SYSTEM span itself may.
			if _, err := AuthorizeLifecycleCommand(tx, system, "task", command(domain.LifecycleResolve, "sys-goal", domain.AuthoritySystem)); err != nil {
				t.Errorf("SYSTEM span: %v", err)
			}
			// A span that outranks its caller is rejected outright.
			_, err = AuthorizeLifecycleCommand(tx, user, "task", command(domain.LifecycleResolve, "user-goal", domain.AuthoritySystem))
			if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Errorf("span outranks caller: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			if got, err := AuthorizeLifecycleCommand(tx, user, "task", command(domain.LifecycleUnpin, "user-pin", domain.AuthorityUser)); err != nil || got.Command.ResolvedItemID != "user-pin-1" {
				t.Errorf("USER Unpin = %+v, %v", got, err)
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if tx.LastSeq() != before {
				t.Errorf("LastSeq moved: a parsed command is never executed")
			}
			g, err := tx.Item("user-goal-1")
			if err != nil {
				return err
			}
			if *g.GoalStatus != domain.GoalOpen || g.Version != 1 {
				t.Errorf("goal changed: %+v", g)
			}
			return nil
		})
	})
}

// TestD1_AuthorizeLifecycleCommand_Grant: an in-force action-specific grant
// issued by a principal with authority over the target authorizes the
// source actor (FR-AUTH-002); a revoked one does not.
func TestD1_AuthorizeLifecycleCommand_Grant(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d1-grant"
		lifecycleFixture(t, s, sess)
		system := principal(sess, domain.AuthoritySystem)
		update(t, s, sess, func(tx store.Tx) error {
			g := storetest.NewGrant(sess, "grant-1", tx.NextSeq(), "sys-goal-1")
			g.Issuer = system
			return tx.InsertGrant(g)
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			got, err := AuthorizeLifecycleCommand(tx, system, "task", command(domain.LifecycleResolve, "sys-goal", domain.AuthorityUser))
			if err != nil || got.GrantID != "grant-1" || got.Command.ResolvedItemID != "sys-goal-1" {
				t.Errorf("granted = %+v, %v; want grant-1", got, err)
			}
			// The grant names Resolve only.
			if _, err := AuthorizeLifecycleCommand(tx, system, "task", command(domain.LifecycleUnpin, "sys-goal", domain.AuthorityUser)); err == nil {
				t.Errorf("Unpin authorized by a Resolve grant")
			}
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			_, err := tx.RevokeGrant("grant-1", domain.LifecycleEvent{
				ID: "revoke-1", SessionID: sess, Seq: tx.NextSeq(), TargetKind: domain.TargetGrant,
				TargetID: "grant-1", Action: "revoke", Actor: system,
			})
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			_, err := AuthorizeLifecycleCommand(tx, system, "task", command(domain.LifecycleResolve, "sys-goal", domain.AuthorityUser))
			if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Errorf("revoked grant: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			return nil
		})
	})
}

// TestD1_AuthorizeLifecycleCommand_Targets: unknown and inaccessible
// targets fail with the identical bare domain.ErrNotFound; several
// accessible current versions are ErrAmbiguousDirective; a target of the
// wrong kind or state for the action is ErrLifecycleTargetMismatch.
func TestD1_AuthorizeLifecycleCommand_Targets(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d1-targets"
		lifecycleFixture(t, s, sess)
		user := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			// A second, workflow-scoped current version of user-goal makes
			// the bare directive ID ambiguous.
			wf := goalLike(sess, "user-goal-wf", "user-goal", tx.NextSeq(), "User goal")
			wf.Scope = domain.ScopeWorkflow
			wf.Access = domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: sess, WorkflowID: "wf"}
			resolved := goalLike(sess, "done-goal-1", "done-goal", tx.NextSeq(), "Done")
			done := domain.GoalResolved
			resolved.GoalStatus = &done
			mustInsert(t, tx, wf, resolved)
			mustFile(t, tx, wf, resolved)
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			for _, target := range []string{"no-such-goal", "hidden-goal", "hidden-goal-1"} {
				_, err := AuthorizeLifecycleCommand(tx, user, "task", command(domain.LifecycleResolve, target, domain.AuthorityUser))
				if err != domain.ErrNotFound {
					t.Errorf("Resolve(%s): err = %v, want bare ErrNotFound", target, err)
				}
			}
			if _, err := AuthorizeLifecycleCommand(tx, user, "task", command(domain.LifecycleResolve, "user-goal", domain.AuthorityUser)); !errors.Is(err, ErrAmbiguousDirective) {
				t.Errorf("ambiguous: err = %v", err)
			}
			for _, c := range []domain.LifecycleCommand{
				command(domain.LifecycleResolve, "user-pin", domain.AuthorityUser),
				command(domain.LifecycleUnpin, "user-goal-1", domain.AuthorityUser),
				command(domain.LifecycleResolve, "done-goal", domain.AuthorityUser),
			} {
				if _, err := AuthorizeLifecycleCommand(tx, user, "task", c); !errors.Is(err, ErrLifecycleTargetMismatch) {
					t.Errorf("%s %s: err = %v, want ErrLifecycleTargetMismatch", c.Action, c.TargetID, err)
				}
			}
			return nil
		})
	})
}
