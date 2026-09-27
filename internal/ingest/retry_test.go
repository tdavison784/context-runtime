package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// These extend TestRetryIdentity_D14 and TestConcurrentRetries_D14 (FR-ING-006,
// D14) to directive-rich receipts, every payload field, sessions, anonymous
// occurrences, and concurrency without a conflicting submitter.

// snapshot counts what an event could have written.
type snapshot struct {
	seq           uint64
	items, rels   int
	obligations   int
	currentPinned []string
}

func (f *fixture) snapshot() snapshot {
	f.t.Helper()
	var s snapshot
	f.view(func(tx store.ReadTx) error {
		s.seq = tx.LastSeq()
		items, err := tx.Items(store.ItemFilter{})
		if err != nil {
			return err
		}
		s.items = len(items)
		rels, err := tx.Relationships(store.RelationshipFilter{})
		if err != nil {
			return err
		}
		s.rels = len(rels)
		obs, err := tx.Obligations("")
		s.obligations = len(obs)
		return err
	})
	s.currentPinned = currentIDs(f.t, f.s, domain.KindConstraint)
	return s
}

// richEvent carries every receipt feature: items of each section, an
// obligation, a resolvable and an unknown lifecycle target, and diagnostics.
func richEvent(id string) domain.Event {
	return userEvent(id, exampleTextNoT("sdd-example")+"\n## Resolve [missing]\n## Remember\n- {ttl=0} bad ttl\n", true)
}

// exampleTextNoT reads an example's input without a *testing.T.
func exampleTextNoT(name string) string {
	b, err := os.ReadFile(filepath.Join(exampleRoot, name, "input.md"))
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestRetryIdentity_RichReceipt(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("w0", "## Working\n- earlier state\n", true))
		first := f.mustIngest(user, richEvent("rich"))
		if len(first.Lifecycle) != 2 || len(first.Diagnostics) == 0 || len(first.Replacements) == 0 {
			t.Fatalf("event is not directive-rich: %+v", first)
		}
		before := f.snapshot()
		again := f.mustIngest(user, richEvent("rich"))
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("retry receipt differs")
		}
		if after := f.snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("retry wrote state: %+v -> %+v", before, after)
		}

		// A later event replaces the pins; the retry still returns the
		// original receipt and neither re-supersedes nor revives anything.
		f.mustIngest(user, userEvent("later", "## Pinned\n- [api] Replaced API rule.\n", true))
		mid := f.snapshot()
		if again := f.mustIngest(user, richEvent("rich")); !reflect.DeepEqual(first, again) {
			t.Fatal("retry after a later replacement differs")
		}
		if after := f.snapshot(); !reflect.DeepEqual(mid, after) {
			t.Fatalf("retry after replacement wrote state: %+v -> %+v", mid, after)
		}
	})
}

// TestRetryConflict_EveryPayloadField: any change to the canonical request
// under the same EventID is a bare ErrEventIDConflict that writes nothing,
// and the original receipt stays retrievable.
func TestRetryConflict_EveryPayloadField(t *testing.T) {
	base := func() domain.Event { return richEvent("c") }
	mutations := map[string]func(*domain.Principal, *domain.Event){
		"span authority": func(_ *domain.Principal, e *domain.Event) {
			e.Spans[0].Authority = domain.AuthorityAgent
			e.Spans[0].DirectiveCapable = false
		},
		"event kind": func(p *domain.Principal, e *domain.Event) {
			p.Authority = domain.AuthorityHarness
			e.Kind = domain.EventHarness
		},
		"turn boundary": func(p *domain.Principal, e *domain.Event) {
			p.Authority = domain.AuthorityHarness
			e.Kind = domain.EventHarness
			e.TurnBoundary = true
		},
		"access boundary": func(_ *domain.Principal, e *domain.Event) {
			e.Spans[0].Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "T", AgentID: "A"}
		},
		"media type":    func(_ *domain.Principal, e *domain.Event) { e.Spans[0].Parts[0].MediaType = "text/markdown" },
		"one more byte": func(_ *domain.Principal, e *domain.Event) { e.Spans[0].Parts[0].Text += " " },
		"extra part": func(_ *domain.Principal, e *domain.Event) {
			e.Spans[0].Parts = append(e.Spans[0].Parts, domain.InputPart{Type: domain.PartText, Text: "x"})
		},
		"extra span": func(_ *domain.Principal, e *domain.Event) {
			e.Spans = append(e.Spans, textSpan(domain.AuthorityTool, false, "x"))
		},
		"source": func(_ *domain.Principal, e *domain.Event) {
			e.Spans[0].Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "notes.md"}
		},
		"principal workflow":  func(p *domain.Principal, _ *domain.Event) { p.WorkflowID = "wf2" },
		"principal agent":     func(p *domain.Principal, _ *domain.Event) { p.AgentID = "B" },
		"principal authority": func(p *domain.Principal, _ *domain.Event) { p.Authority = domain.AuthoritySystem },
	}
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		first := f.mustIngest(user, base())
		for name, mutate := range mutations {
			p, e := user, base()
			mutate(&p, &e)
			before := f.snapshot()
			if _, err := f.ingest(p, e); err != domain.ErrEventIDConflict {
				t.Errorf("%s: err = %v, want bare ErrEventIDConflict", name, err)
			}
			if after := f.snapshot(); !reflect.DeepEqual(before, after) {
				t.Errorf("%s: conflicting retry wrote state", name)
			}
		}
		if again := f.mustIngest(user, base()); !reflect.DeepEqual(first, again) {
			t.Fatal("original receipt changed after conflicts")
		}
	})
}

// TestRetryIdentity_SessionScoped: an EventID is an idempotency key within
// a session only; the same EventID in another session is a new event.
func TestRetryIdentity_SessionScoped(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		a := f.mustIngest(principal(domain.AuthorityUser), userEvent("shared", "session S", false))
		other := principal(domain.AuthorityUser)
		other.SessionID = "S2"
		e := userEvent("shared", "session S2, different text", false)
		e.Spans[0].Access.SessionID = "S2"
		b, err := f.in.Ingest(ctx, f.s, other, e)
		if err != nil {
			t.Fatal(err)
		}
		if a.OccurrenceID == b.OccurrenceID || slices.Equal(a.ItemIDs(), b.ItemIDs()) {
			t.Fatal("sessions share an idempotency key")
		}
	})
}

// TestRetryIdentity_SessionScopedRich (TEST-1.2): the same EventID carrying
// a directive-rich event in two sessions yields two independent events.
// Item IDs are disjoint, obligations and lifecycle outcomes stay in their
// own session, and each session's retry returns only its own receipt.
func TestRetryIdentity_SessionScopedRich(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		inSession := func(s string) (domain.Principal, domain.Event) {
			p := principal(domain.AuthorityUser)
			p.SessionID = s
			e := richEvent("shared")
			for i := range e.Spans {
				e.Spans[i].Access.SessionID = s
			}
			return p, e
		}
		pa, ea := inSession(sess)
		pb, eb := inSession("S2")
		ra, err := f.in.Ingest(ctx, f.s, pa, ea)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := f.in.Ingest(ctx, f.s, pb, eb)
		if err != nil {
			t.Fatal(err)
		}
		if ra.OccurrenceID == rb.OccurrenceID || slices.ContainsFunc(rb.ItemIDs(), func(id string) bool { return slices.Contains(ra.ItemIDs(), id) }) {
			t.Fatal("sessions share item IDs or occurrences")
		}
		counts := map[string]int{}
		for _, pair := range []struct {
			s string
			r domain.IngestReceipt
		}{{sess, ra}, {"S2", rb}} {
			own := pair.r.ItemIDs()
			for _, it := range pair.r.Items {
				if it.SessionID != pair.s || it.Access.SessionID != pair.s {
					t.Fatalf("%s: item in another session: %+v", pair.s, it)
				}
			}
			for _, c := range pair.r.Lifecycle {
				if c.SessionID != pair.s || c.ResolvedItemID != "" && !slices.Contains(own, c.ResolvedItemID) {
					t.Fatalf("%s: lifecycle outcome crosses sessions: %+v", pair.s, c)
				}
			}
			if err := f.s.View(ctx, pair.s, func(tx store.ReadTx) error {
				obs, err := tx.Obligations("")
				if err != nil {
					return err
				}
				// Explicit obligation= and claim-pattern pins both declare
				// (P3-12); each session has its own, from its own items.
				counts[pair.s] = len(obs)
				for _, o := range obs {
					if len(obs) == 0 || o.SessionID != pair.s || !slices.Contains(own, o.SourceItemID) {
						t.Fatalf("%s: obligations = %+v", pair.s, obs)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if counts[sess] == 0 || counts[sess] != counts["S2"] {
			t.Fatalf("obligations per session = %v", counts)
		}
		// Each session's retry returns its own original receipt.
		if again, err := f.in.Ingest(ctx, f.s, pa, ea); err != nil || !reflect.DeepEqual(again, ra) {
			t.Fatalf("session S retry: %v", err)
		}
		if again, err := f.in.Ingest(ctx, f.s, pb, eb); err != nil || !reflect.DeepEqual(again, rb) {
			t.Fatalf("session S2 retry: %v", err)
		}
	})
}
