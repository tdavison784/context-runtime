package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// w4Policy names W4's registries, so New wires the real declaration hook.
func w4Policy() domain.Phase3Policy {
	p := testPolicy()
	p.Claim, p.Matcher, p.ObservationState = policy.ClaimPatternVersion, policy.MatcherRegistryVersion, policy.ObservationStateRule
	return p
}

// seedW4Pin files pinned directive "prior" exactly as ingest does: creation
// declaration, first-version filing, then W4's own declaration of its claim.
func seedW4Pin(t *testing.T, db store.Store, text string, attrs []string) *domain.ObligationRef {
	t.Helper()
	return seedW4PinAs(t, db, domain.AuthorityUser, text, attrs)
}

func seedW4PinAs(t *testing.T, db store.Store, authority domain.Authority, text string, attrs []string) *domain.ObligationRef {
	t.Helper()
	w4, err := obligation.New(w4Policy(), obligation.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	system := storetest.NewPrincipal("s", domain.AuthoritySystem)
	var ref *domain.ObligationRef
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		it := storetest.NewDirective("s", "prior", "d", tx.NextSeq(), text)
		it.Namespace, it.Section, it.Authority = domain.NamespaceDirective, domain.SectionPinned, authority
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if _, err := graph.DeclareCreation(tx, it, graph.CreationAcceptance{PolicyVersion: "policy/v1", AcceptedAttributes: attrs}); err != nil {
			return err
		}
		if _, err := graph.ReplaceDirective(tx, system, "task", "d", it.ID, it.EventID); err != nil {
			return err
		}
		ref, err = w4.DeclareForReplacementTx(tx, system, it.ID, tx.NextSeq())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestReplaceDirectiveDeclaresRealW4Obligation(t *testing.T) {
	ctx := context.Background()
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	for name, tc := range map[string]struct {
		prior, next string
		attrs       []string
		declares    bool
	}{
		"attribute claim":    {"Keep the build green.", "Keep the build green.", []string{"obligation=tests_pass"}, true},
		"text claim pattern": {"All tests must pass.", "All tests must pass.", nil, true},
		"plain pin":          {"Ship it.", "Ship it now.", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				v1 := seedW4Pin(t, db, tc.prior, tc.attrs)
				s, err := New(db, w4Policy())
				if err != nil {
					t.Fatal(err)
				}
				intent := replaceIntent("r", 1, tc.next)
				intent.AcceptedAttributes = tc.attrs
				out, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
				if err != nil {
					t.Fatal(err)
				}
				fresh := out.Result.Records.IDs[0]
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					obs, err := tx.ObligationsBySource(fresh, 8)
					if err != nil {
						return err
					}
					if !tc.declares {
						if len(obs) != 0 {
							t.Fatalf("plain pin declared %+v", obs)
						}
						return nil
					}
					if len(obs) != 1 || !obs[0].Current || obs[0].Status != domain.ObligationUnresolved {
						t.Fatalf("replacement obligation: %+v", obs)
					}
					if v1 != nil {
						if obs[0].ObligationID != v1.ObligationID || obs[0].Version != v1.Version+1 {
							t.Fatalf("not the next version of %+v: %+v", v1, obs[0])
						}
						old, err := tx.ObligationsBySource("prior", 8)
						if err != nil || len(old) != 1 || old[0].Current {
							t.Fatalf("prior obligation not retired: %+v %v", old, err)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if again, err := s.ReplaceDirectiveStandalone(ctx, user, intent); err != nil || again.MutationReceiptID != out.MutationReceiptID {
					t.Fatalf("replay: %+v %v", again, err)
				}
			})
		})
	}
}
