package ingest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

func exampleText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(exampleRoot, name, "input.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// currentIDs returns the current items of kind k in task T.
func currentIDs(t *testing.T, s store.Store, k domain.Kind) []string {
	t.Helper()
	var out []string
	if err := s.View(ctx, sess, func(tx store.ReadTx) error {
		items, err := tx.Items(store.ItemFilter{TaskID: "T", Kinds: []domain.Kind{k}})
		if err != nil {
			return err
		}
		for _, it := range items {
			if ok, err := graph.IsCurrent(tx, it.ID); err != nil {
				return err
			} else if ok {
				out = append(out, it.ID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestT18_EndToEnd runs trace T18 as one session on both stores, using the
// canonical example bytes (testdata/directives/t18-*): a non-capable paste,
// the same paste marked directive-capable, then Working snapshots W1 and
// W2. SQLite is reopened at the end so the final state is proven durable.
func TestT18_EndToEnd(t *testing.T) {
	doc := exampleText(t, "t18-pasted-document")
	w1, w2 := exampleText(t, "t18-working-w1"), exampleText(t, "t18-working-w2")
	path := filepath.Join(t.TempDir(), "t18.db")
	stores := map[string]func() store.Store{
		"memory": func() store.Store { return memory.New() },
		"sqlite": func() store.Store {
			s, err := sqlite.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	}
	for _, name := range []string{"memory", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			s := stores[name]()
			f := newFixture(t, s)
			user := principal(domain.AuthorityUser)

			// Step 1: ordinary chat. One user_message; no goal, pin,
			// obligation or command; the skipped headings are diagnosed.
			r1 := f.mustIngest(user, userEvent("t18-paste", doc, false))
			if len(r1.Items) != 1 || r1.Items[0].Kind != domain.KindUserMessage || r1.Items[0].Parts[0].Text != doc || len(r1.Lifecycle) != 0 {
				t.Fatalf("step 1 receipt: %+v", r1)
			}
			notParsed := 0
			for _, d := range r1.Diagnostics {
				if d.Code == domain.DirectiveNotParsed && d.Reason == domain.ReasonSourceNotCapable {
					notParsed++
				}
			}
			if notParsed != 3 || len(currentIDs(t, s, domain.KindGoal)) != 0 || len(currentIDs(t, s, domain.KindConstraint)) != 0 {
				t.Fatalf("step 1: %d diagnostics, goals %v, pins %v", notParsed, currentIDs(t, s, domain.KindGoal), currentIDs(t, s, domain.KindConstraint))
			}

			// Repeat step 1 marked capable: items with USER authority, and
			// nothing higher. The earlier transcript gains nothing.
			r2 := f.mustIngest(user, userEvent("t18-paste-capable", doc, true))
			for _, it := range r2.Items {
				if it.Authority != domain.AuthorityUser || it.Access != taskAccess() {
					t.Fatalf("capable paste item above USER or outside the span boundary: %+v", it)
				}
			}
			goals, pins := currentIDs(t, s, domain.KindGoal), currentIDs(t, s, domain.KindConstraint)
			if len(goals) != 1 || len(pins) != 1 {
				t.Fatalf("capable paste: goals %v pins %v", goals, pins)
			}

			// Step 2: W1 {two items}, then W2 {one item}. W2's item
			// supersedes both W1 items; only it is current task_state.
			f.mustIngest(user, userEvent("t18-w1", w1, true))
			old := currentIDs(t, s, domain.KindTaskState)
			if len(old) != 2 {
				t.Fatalf("W1 current = %v", old)
			}
			rw2 := f.mustIngest(user, userEvent("t18-w2", w2, true))
			sem := semantic(rw2)
			if len(sem) != 1 || len(rw2.Replacements) != 2 {
				t.Fatalf("W2 receipt: items %+v replacements %+v", sem, rw2.Replacements)
			}
			if got := currentIDs(t, s, domain.KindTaskState); !slices.Equal(got, []string{sem[0].ID}) {
				t.Fatalf("current task_state = %v, want only %s", got, sem[0].ID)
			}
			f.view(func(tx store.ReadTx) error {
				edges, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: sem[0].ID})
				if err != nil {
					return err
				}
				var targets []string
				for _, e := range edges {
					targets = append(targets, e.ToID)
				}
				slices.Sort(targets)
				want := slices.Clone(old)
				slices.Sort(want)
				if !slices.Equal(targets, want) {
					t.Errorf("SUPERSEDES from W2 = %v, want both W1 items %v", targets, want)
				}
				return nil
			})
			// The Working snapshot touches only Working items.
			if !slices.Equal(currentIDs(t, s, domain.KindGoal), goals) || !slices.Equal(currentIDs(t, s, domain.KindConstraint), pins) {
				t.Fatal("Working snapshot retired a non-Working item")
			}

			if name == "sqlite" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = stores[name]()
				if got := currentIDs(t, s, domain.KindTaskState); !slices.Equal(got, []string{sem[0].ID}) {
					t.Fatalf("after restart current task_state = %v", got)
				}
				// A retry after restart still returns the original receipt.
				f = newFixture(t, s)
				if again := f.mustIngest(user, userEvent("t18-w2", w2, true)); !slices.Equal(again.ItemIDs(), rw2.ItemIDs()) || again.Seq != rw2.Seq {
					t.Fatal("retry after restart changed the receipt")
				}
			}
			s.Close()
		})
	}
}
