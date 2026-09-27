package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedDirective files a current DIRECTIVE goal "prior" with its creation
// declaration unless legacy, plus an obligation bound to it.
func seedDirective(t *testing.T, mem store.Store, authority domain.Authority, legacy bool) domain.ContextItem {
	t.Helper()
	var it domain.ContextItem
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		it = storetest.NewDirective("s", "prior", "d", tx.NextSeq(), "ship the release")
		open := domain.GoalOpen
		it.Namespace, it.Kind, it.Section, it.GoalStatus, it.Authority = domain.NamespaceDirective, domain.KindGoal, domain.SectionGoal, &open, authority
		it.Generation, it.Retention = domain.GenerationDurable, domain.RetentionHigh
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := tx.SetCurrentVersion(it.ID); err != nil {
			return err
		}
		if !legacy {
			if _, err := graph.DeclareCreation(tx, it, graph.CreationAcceptance{PolicyVersion: "policy/v1"}); err != nil {
				return err
			}
		}
		o := storetest.NewObligation("s", "o", 1, tx.NextSeq(), it.ID)
		o.SourceAuthority = authority
		return tx.InsertObligationVersion(o)
	}); err != nil {
		t.Fatal(err)
	}
	return it
}

func replaceIntent(req string, version uint64, text string) domain.ReplaceDirectiveIntent {
	return domain.ReplaceDirectiveIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: req, ItemID: "prior", ExpectedVersion: version},
		Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}}
}

func TestReplaceDirectiveReopensWithIdenticalContentAndRetiresObligations(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	prior := seedDirective(t, mem, domain.AuthorityUser, false)
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	if _, err := s.ResolveStandalone(ctx, user, domain.ResolveIntent{RequestID: "resolve", ItemID: "prior", ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("stale", 1, "ship the release")); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	intent := replaceIntent("r", 2, "ship the release")
	out, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
	if err != nil {
		t.Fatal(err)
	}
	rec := out.Result.Records
	if out.MutationReceiptID == "" || out.GrantID != "" || rec == nil || rec.Validate() != nil || rec.Kind != "REPLACEMENT" || rec.IDs[2] != "prior" {
		t.Fatalf("outcome: %+v", out)
	}
	fresh := rec.IDs[0]
	var last uint64
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		last = tx.LastSeq()
		n, err := tx.Item(fresh)
		if err != nil {
			return err
		}
		if n.ContentHash != prior.ContentHash || *n.GoalStatus != domain.GoalOpen || n.Generation != domain.GenerationDurable || n.Authority != domain.AuthorityUser ||
			n.Access != prior.Access || n.DirectiveID != "d" || n.Version != 1 {
			t.Fatalf("replacement occurrence: %+v", n)
		}
		for id, want := range map[string]bool{fresh: true, "prior": false} {
			if current, err := graph.IsCurrent(tx, id); err != nil || current != want {
				t.Fatalf("%s current=%v %v", id, current, err)
			}
		}
		o, err := tx.Obligation("o")
		if err != nil || o.Current {
			t.Fatalf("bound obligation not retired: %+v %v", o, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
	if err != nil || again.MutationReceiptID != out.MutationReceiptID || again.Result.Records.IDs[0] != fresh {
		t.Fatalf("replay: %+v %v", again, err)
	}
	changed := replaceIntent("r", 2, "different text")
	if _, err := s.ReplaceDirectiveStandalone(ctx, user, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed arguments replayed: %v", err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != last {
			t.Fatal("replay allocated a sequence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r2", 2, "again")); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("historical occurrence replaced: %v", err)
	}
}

func TestReplaceDirectiveNeedsAuthorityOrExactGrant(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	seedDirective(t, mem, domain.AuthoritySystem, false)
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	for _, p := range []domain.Principal{harness, storetest.NewPrincipal("s", domain.AuthorityUser), storetest.NewPrincipal("s", domain.AuthorityAgent)} {
		if _, err := s.ReplaceDirectiveStandalone(ctx, p, replaceIntent("r", 1, "new text")); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("%s replaced SYSTEM directive: %v", p.Authority, err)
		}
	}
	grantTo(t, mem, "replace-grant", domain.ActionReplaceDirective, "prior", harness)
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		// The indirect retirement is authorized separately on the obligation.
		return tx.InsertGrant(domain.MutationGrant{ID: "retire-grant", SessionID: "s", Action: domain.ActionReplaceDirective,
			Targets: []domain.GrantTarget{domain.ObligationGrantTarget("s", "o", 1)}, Issuer: storetest.NewPrincipal("s", domain.AuthoritySystem), Grantee: &harness, IssuedSeq: tx.NextSeq()})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReplaceDirectiveStandalone(ctx, harness, replaceIntent("r", 1, "new text"))
	if err != nil || out.GrantID != "replace-grant" {
		t.Fatalf("granted replacement: %+v %v", out, err)
	}
}

func TestReplaceDirectiveFailsClosedOnUnknownIdentity(t *testing.T) {
	ctx := context.Background()
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	for name, tc := range map[string]struct {
		legacy bool
		intent domain.ReplaceDirectiveIntent
		want   error
	}{
		"legacy source": {true, replaceIntent("r", 1, "x"), domain.ErrUnsupportedSchema},
		"changed attributes": {false, func() domain.ReplaceDirectiveIntent {
			i := replaceIntent("r", 1, "x")
			i.AcceptedAttributes = []string{"scope"}
			return i
		}(), domain.ErrInvalidTransition},
		"missing target": {false, func() domain.ReplaceDirectiveIntent { i := replaceIntent("r", 1, "x"); i.ItemID = "nope"; return i }(), domain.ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			s, _ := New(mem, testPolicy())
			seedDirective(t, mem, domain.AuthorityUser, tc.legacy)
			if _, err := s.ReplaceDirectiveStandalone(ctx, user, tc.intent); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				if current, _ := graph.IsCurrent(tx, "prior"); !current {
					t.Fatal("failed replacement retired the prior version")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// SPEC-1.6: an enabled SUPERSESSION trigger persists a stable GC request
// from the replacement's own transaction; a disabled one persists nothing.
func TestReplaceDirectiveEnqueuesSupersessionGC(t *testing.T) {
	ctx := context.Background()
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	for _, enabled := range []bool{true, false} {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		seedDirective(t, mem, domain.AuthorityUser, false)
		pol := testPolicy()
		pol.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
		if enabled {
			pol.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCSupersession, domain.GCTaskCompletion} // sorted
		}
		s, err := New(mem, pol)
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r", 1, "ship the release"))
		if err != nil {
			t.Fatal(err)
		}
		pending := pendingGC(t, mem)
		if !enabled {
			if len(pending) != 0 {
				t.Fatalf("disabled trigger enqueued: %+v", pending)
			}
			continue
		}
		if len(pending) != 1 || pending[0].Trigger != domain.GCSupersession || pending[0].Scope != domain.CollectTask || pending[0].TaskID != "task" || pending[0].Origin != user {
			t.Fatalf("supersession GC request: %+v", pending)
		}
		// Replay neither re-enqueues nor conflicts.
		if again, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r", 1, "ship the release")); err != nil || again.MutationReceiptID != out.MutationReceiptID || len(pendingGC(t, mem)) != 1 {
			t.Fatalf("replay: %v", err)
		}
	}
}
