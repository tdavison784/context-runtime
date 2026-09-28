package ingest

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// P3-1 (ADR 8 :1197): inside ONE multi-command event, grant validity is
// evaluated at each command's own committed sequence — inclusive at
// ExpiresAtSeq, dead from RevokedSeq on — and a command whose grant is no
// longer in force aborts and rolls back the whole event, including the
// commands that already executed and the mid-event revocation itself.

// p1Store builds a fresh fixture of the given kind under the test policy,
// the way semanticStores does, so one test can calibrate on one store and
// probe on identically-seeded siblings.
func p1Store(t *testing.T, kind string) *fixture {
	t.Helper()
	var s store.Store
	if kind == "memory" {
		ms := memory.New()
		t.Cleanup(func() { ms.Close() })
		s = ms
	} else {
		s = sqlitetest.Open(t)
	}
	f := newFixture(t, s)
	f.usePolicy(testPolicy())
	return f
}

// p1Seed ingests two SYSTEM goals and one SYSTEM-issued grant that lets a
// USER actor resolve both, expiring at expiresAt (0: never). Resolving a
// SYSTEM goal is beyond USER authority, so every Resolve below runs through
// this grant alone. The seeding is deterministic, so identically-seeded
// stores allocate identical sequences.
func p1Seed(t *testing.T, kind string, expiresAt uint64) *fixture {
	t.Helper()
	f := p1Store(t, kind)
	sys := principal(domain.AuthoritySystem)
	sb1 := mustDirective(t, f.mustIngest(sys, sysEvent("p1-g1", "## Goal [sb1]\nSystem goal one.\n")), "sb1").ID
	sb2 := mustDirective(t, f.mustIngest(sys, sysEvent("p1-g2", "## Goal [sb2]\nSystem goal two.\n")), "sb2").ID
	user := principal(domain.AuthorityUser)
	err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: "p1-grant", SessionID: sess, Action: domain.ActionResolve,
			Targets:   []domain.GrantTarget{domain.ItemGrantTarget(sess, sb1), domain.ItemGrantTarget(sess, sb2)},
			Issuer:    sys,
			Grantee:   &user,
			IssuedSeq: tx.NextSeq(), ExpiresAtSeq: expiresAt,
		})
	})
	if err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return f
}

// p1AssertOpen asserts the named directives are still OPEN goals after an
// aborted event and that the abort left no lifecycle events after seq from.
func p1AssertOpen(t *testing.T, f *fixture, from uint64, names ...string) {
	t.Helper()
	f.view(func(tx store.ReadTx) error {
		for _, name := range names {
			it, ok := currentDirective(t, tx, name)
			if !ok {
				t.Fatalf("no current directive %s after abort", name)
			}
			if it.GoalStatus == nil || *it.GoalStatus != domain.GoalOpen {
				t.Errorf("%s = %v after abort; an executed command was not rolled back", name, it.GoalStatus)
			}
		}
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{MinSeq: from + 1})
		if err != nil {
			return err
		}
		if n := len(evs); n != 0 {
			t.Errorf("aborted event left %d lifecycle events after seq %d", n, from)
		}
		return nil
	})
}

// TestP3_1_GrantExpiringMidEvent: a two-command event whose grant expires
// between its commands. The control run calibrates the two Resolve
// sequences on a live grant; two identically-seeded sibling stores then hold
// grants expiring exactly at the second command's sequence (the event still
// succeeds — expiry is INCLUSIVE at ExpiresAtSeq) and at the first command's
// sequence (the first command still executes at the boundary, the second is
// dead, and the whole event aborts and rolls back).
func TestP3_1_GrantExpiringMidEvent(t *testing.T) {
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			sys := principal(domain.AuthoritySystem)
			text := "## Resolve [sb1]\n## Resolve [sb2]\n"

			// Control: with a never-expiring grant both commands execute;
			// their committed sequences calibrate the probes.
			ctl := p1Seed(t, kind, 0)
			before := ctl.lastSeq()
			r := ctl.mustIngest(sys, userEvent("p1-ctl", text, true))
			if len(r.Lifecycle) != 2 || r.Lifecycle[0].Status != domain.CommandExecuted || r.Lifecycle[1].Status != domain.CommandExecuted ||
				r.Lifecycle[0].Resolution != domain.TargetResolved || r.Lifecycle[1].Resolution != domain.TargetResolved {
				t.Fatalf("control resolves = %+v", r.Lifecycle)
			}
			var seqs []uint64
			ctl.view(func(tx store.ReadTx) error {
				evs, err := tx.LifecycleEvents(store.LifecycleFilter{MinSeq: before + 1})
				if err != nil {
					return err
				}
				for _, ev := range evs {
					if ev.Action == string(domain.ActionResolve) {
						seqs = append(seqs, ev.Seq)
					}
				}
				return nil
			})
			slices.Sort(seqs)
			if len(seqs) != 2 || seqs[0] >= seqs[1] {
				t.Fatalf("control resolve sequences = %v, want two increasing", seqs)
			}
			first, second := seqs[0], seqs[1]

			// Probe A: the grant expires exactly at the second command's
			// sequence. Expiry is inclusive, so BOTH commands still execute
			// and the event succeeds with both goals resolved.
			a := p1Seed(t, kind, second)
			ra := a.mustIngest(sys, userEvent("p1-exp-a", text, true))
			if len(ra.Lifecycle) != 2 || ra.Lifecycle[0].Status != domain.CommandExecuted || ra.Lifecycle[1].Status != domain.CommandExecuted {
				t.Fatalf("expiry at the second command's sequence refused a command: %+v", ra.Lifecycle)
			}
			a.view(func(tx store.ReadTx) error {
				for _, name := range []string{"sb1", "sb2"} {
					it, ok := currentDirective(t, tx, name)
					if !ok || it.GoalStatus == nil || *it.GoalStatus != domain.GoalResolved {
						t.Errorf("probe A: goal %s = %+v (%v), want RESOLVED at the inclusive boundary", name, it.GoalStatus, ok)
					}
				}
				return nil
			})

			// Probe B: the grant expires exactly at the FIRST command's
			// sequence. The first command still executes (inclusive, as probe
			// A proves at its own boundary), the second is dead, and the
			// whole event aborts and rolls back — nothing is written.
			b := p1Seed(t, kind, first)
			bBefore := b.lastSeq()
			if _, err := b.ingest(sys, userEvent("p1-exp-b", text, true)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("grant expired between the commands: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			if got := b.lastSeq(); got != bBefore {
				t.Errorf("aborted event wrote state: seq %d -> %d", bBefore, got)
			}
			p1AssertOpen(t, b, bBefore, "sb1", "sb2")
		})
	}
}

// TestP3_1_GrantRevokedMidEvent: the same two-command event with the grant
// revoked BETWEEN the commands by an in-event REVOKE_GRANT operation. The
// revocation commits at its own sequence, so the first command (before it)
// executes and the second (after it) is dead: the event aborts and rolls
// back everything, including the revocation itself — the grant is left live,
// which the control event then proves by resolving both goals through it.
func TestP3_1_GrantRevokedMidEvent(t *testing.T) {
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			f := p1Seed(t, kind, 0)
			sys := principal(domain.AuthoritySystem)
			spans := []domain.Span{
				textSpan(domain.AuthorityUser, true, "## Resolve [sb1]\n"),
				textSpan(domain.AuthorityUser, true, "## Resolve [sb2]\n"),
			}
			revoke := domain.SemanticOperation{Kind: domain.OperationRevokeGrant,
				RevokeGrant: &domain.RevokeGrantIntent{GrantID: "p1-grant"}}
			before := f.lastSeq()

			if _, err := f.ingest(sys, sysOpsEvent("p1-rev", spans, spanOp(0), revoke, spanOp(1))); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("grant revoked between the commands: err = %v, want ErrInvalidAuthorityPromotion", err)
			}
			if got := f.lastSeq(); got != before {
				t.Errorf("aborted event wrote state: seq %d -> %d", before, got)
			}
			p1AssertOpen(t, f, before, "sb1", "sb2")
			f.view(func(tx store.ReadTx) error {
				g, err := tx.Grant("p1-grant")
				if err != nil {
					return err
				}
				if g.RevokedSeq != 0 {
					t.Errorf("rolled-back event left the grant revoked from seq %d", g.RevokedSeq)
				}
				return nil
			})

			// Control: the same span operations without the revocation execute
			// both commands — through the grant the aborted event restored.
			r := f.mustIngest(sys, sysOpsEvent("p1-rev-ctl", spans, spanOp(0), spanOp(1)))
			if len(r.Lifecycle) != 2 || r.Lifecycle[0].Status != domain.CommandExecuted || r.Lifecycle[1].Status != domain.CommandExecuted {
				t.Fatalf("control resolves = %+v", r.Lifecycle)
			}
		})
	}
}
