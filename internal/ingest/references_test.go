package ingest

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

func TestLocatorKey_M5(t *testing.T) {
	cases := []struct {
		kind      domain.SourceKind
		loc, want string
	}{
		{domain.SourcePath, "docs/architecture.md", "path:docs/architecture.md"},
		{domain.SourcePath, "./docs//architecture.md", "path:docs/architecture.md"},
		{domain.SourcePath, "a/../go.mod", "path:go.mod"},
		{domain.SourcePath, "../outside", ""},
		{domain.SourcePath, "/etc/passwd", ""},
		{domain.SourcePath, "has space", ""},
		{domain.SourcePath, `C:\x`, ""},
		{domain.SourceURL, "https://example.com/a?b", "url:https://example.com/a?b"},
		{domain.SourceURL, "not a url", ""},
		{domain.SourceTool, "x", ""},
	}
	for _, c := range cases {
		got, ok := domain.LocatorKey(c.kind, c.loc)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("LocatorKey(%s, %q) = %q, %v; want %q", c.kind, c.loc, got, ok, c.want)
		}
	}
}

// sourceEvent is a TOOL result whose trusted source names a path.
func sourceEvent(id, locator string, access domain.AccessBoundary) domain.Event {
	s := textSpan(domain.AuthorityTool, false, "file contents of "+locator)
	s.Access = access
	s.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: locator}
	return domain.Event{EventID: id, Kind: domain.EventTool, Spans: []domain.Span{s}}
}

func (f *fixture) references(from string) []string {
	var out []string
	f.view(func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, FromID: from})
		for _, r := range rels {
			out = append(out, r.ToID)
		}
		return err
	})
	return out
}

// TestReferences_M5: a References item links every matching source,
// whether ingested before or after it, never a narrower (private) source,
// and a non-locator text links nothing.
func TestReferences_M5(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		early := f.mustIngest(user, userEvent("u0", "hi", false)) // opens turn 1
		_ = early
		before := f.mustIngest(user, sourceEvent("t1", "go.mod", taskAccess()))
		r := f.mustIngest(user, userEvent("u1", "## References\n- ./go.mod\n- docs/architecture.md\n- the design doc\n", true))
		refs := semantic(r)
		if len(refs) != 3 {
			t.Fatalf("reference items = %d", len(refs))
		}
		if got := f.references(refs[0].ID); len(got) != 1 || got[0] != before.Items[0].ID {
			t.Errorf("go.mod references = %v, want the earlier source", got)
		}
		private := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "T", AgentID: "A"}
		f.mustIngest(user, sourceEvent("t2", "docs/architecture.md", private))
		if got := f.references(refs[1].ID); len(got) != 0 {
			t.Errorf("task-wide reference linked private evidence: %v", got)
		}
		later := f.mustIngest(user, sourceEvent("t3", "docs/./architecture.md", taskAccess()))
		if got := f.references(refs[1].ID); len(got) != 1 || got[0] != later.Items[0].ID {
			t.Errorf("architecture references = %v, want the later source", got)
		}
		if got := f.references(refs[2].ID); len(got) != 0 {
			t.Errorf("non-locator linked: %v", got)
		}
	})
}

// TestReferences_SurviveRestart: unresolved references are persisted, so a
// source ingested after a restart still links.
func TestReferences_SurviveRestart(t *testing.T) {
	path := sqlitetest.Path(t)
	user := principal(domain.AuthorityUser)
	open := func() *fixture {
		s, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return newFixture(t, s)
	}
	f := open()
	f.mustIngest(user, userEvent("u0", "hi", false))
	ref := semantic(f.mustIngest(user, userEvent("u1", "## References\n- go.sum\n", true)))[0]
	f.s.Close()

	f = open()
	src := f.mustIngest(user, sourceEvent("t1", "go.sum", taskAccess()))
	if got := f.references(ref.ID); len(got) != 1 || got[0] != src.Items[0].ID {
		t.Errorf("references after restart = %v", got)
	}
}

// TestReferencesByItemID_F4 is SPEC-1.2 (FR-DIR-003): a References item
// naming an accessible same-session item ID links to it; a missing and an
// inaccessible ID link nothing and produce identical receipts (apart from
// IDs), so references never probe existence; a target narrower than the
// reference is never linked.
func TestReferencesByItemID_F4(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		target := f.mustIngest(user, userEvent("u0", "hello", false)).Items[0]
		r := f.mustIngest(user, userEvent("u1", "## References\n- "+target.ID+"\n", true))
		if got := f.references(semantic(r)[0].ID); len(got) != 1 || got[0] != target.ID {
			t.Errorf("item-ID reference links = %v, want [%s]", got, target.ID)
		}

		private := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "T", AgentID: "A"}
		priv := textSpan(domain.AuthorityUser, false, "private note")
		priv.Access = private
		hidden := f.mustIngest(user, domain.Event{EventID: "p0", Kind: domain.EventUser, Spans: []domain.Span{priv}}).Items[0]

		agentB := user
		agentB.AgentID = "B"
		shape := func(r domain.IngestReceipt) string {
			return fmt.Sprintf("items=%d dups=%d diags=%d links=%d", len(r.Items), len(r.Duplicates), len(r.Diagnostics), len(f.references(semantic(r)[0].ID)))
		}
		hid := f.mustIngest(agentB, userEvent("b1", "## References\n- "+hidden.ID+"\n", true))
		miss := f.mustIngest(agentB, userEvent("b2", "## References\n- itm_00000000000000000000000000000000\n", true))
		if shape(hid) != shape(miss) || len(f.references(semantic(hid)[0].ID)) != 0 {
			t.Errorf("inaccessible %s vs missing %s", shape(hid), shape(miss))
		}

		// Agent A can see its private note, but a task-wide reference must
		// not disclose it to the rest of the task.
		wide := f.mustIngest(user, userEvent("a1", "## References\n- "+hidden.ID+"\n", true))
		if got := f.references(semantic(wide)[0].ID); len(got) != 0 {
			t.Errorf("task-wide reference linked a private item: %v", got)
		}
	})
}

// TestReferenceLinkBudget_Ruling1: optional REFERENCES edges have their own
// per-event budget; reaching it stops linking with a
// reference_links_truncated diagnostic, and optional links never consume
// MaxRelationships, so they can never make an essential edge reject the
// event.
func TestReferenceLinkBudget_Ruling1(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		for i := range 4 {
			// Distinct content: identical re-reads would be duplicates of
			// the first read, not separate live sources.
			e := sourceEvent(fmt.Sprintf("s%d", i), "a.md", taskAccess())
			e.Spans[0].Parts[0].Text = fmt.Sprintf("a.md revision %d", i)
			f.mustIngest(user, e)
		}

		f.in.Limits = domain.Limits{MaxReferenceLinks: 2}
		r := f.mustIngest(user, userEvent("r1", "## References\n- a.md\n", true))
		if got := f.references(semantic(r)[0].ID); len(got) != 2 {
			t.Errorf("links = %d, want the budget of 2", len(got))
		}
		truncated := 0
		for _, d := range r.Diagnostics {
			if d.Code == domain.ReferenceLinksTruncated && d.Reason == domain.ReasonReferenceLinksTruncated {
				truncated++
			}
		}
		if truncated != 1 {
			t.Errorf("truncation diagnostics = %d, want 1", truncated)
		}

		// Two essential DERIVED_FROM edges exactly fill MaxRelationships;
		// four optional links must not push either out.
		f.in.Limits = domain.Limits{MaxRelationships: 2}
		r2, err := f.ingest(user, userEvent("r2", "## References\n- ./a.md\n## Remember\n- essential fact\n", true))
		if err != nil {
			t.Fatalf("optional links rejected the event: %v", err)
		}
		if got := f.references(semantic(r2)[0].ID); len(got) != 4 {
			t.Errorf("links = %d, want all 4 within the default budget", len(got))
		}
		// The budget is an execution limit the receipt records.
		if r.Versions.Limits.MaxReferenceLinks != 2 || r2.Versions.Limits.MaxReferenceLinks != 256 {
			t.Errorf("recorded budgets = %d, %d; want 2, 256", r.Versions.Limits.MaxReferenceLinks, r2.Versions.Limits.MaxReferenceLinks)
		}
	})
}

// TestReferenceLinkTruncationReporting_SPEC23_DUR22: truncation is reported
// exactly when a linkable candidate meets an exhausted budget: for item-ID
// references too (SPEC-2.3), and never when the only remaining candidates
// could not have been linked anyway (DUR-2.2).
func TestReferenceLinkTruncationReporting_SPEC23_DUR22(t *testing.T) {
	count := func(r domain.IngestReceipt) int {
		n := 0
		for _, d := range r.Diagnostics {
			if d.Code == domain.ReferenceLinksTruncated {
				n++
			}
		}
		return n
	}
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		a := f.mustIngest(user, userEvent("a", "item a", false)).Items[0]
		b := f.mustIngest(user, userEvent("b", "item b", false)).Items[0]
		f.in.Limits = domain.Limits{MaxReferenceLinks: 1}
		r := f.mustIngest(user, userEvent("ids", "## References\n- "+a.ID+"\n- "+b.ID+"\n", true))
		if links := len(f.references(semantic(r)[0].ID)) + len(f.references(semantic(r)[1].ID)); links != 1 || count(r) != 1 {
			t.Errorf("item-ID references at the budget: links %d, truncation diagnostics %d; want 1, 1", links, count(r))
		}

		// A task-wide source is linkable; an agent-private one never is
		// from a task-wide reference. Spending the budget on the first
		// must not report the second as truncated.
		f.in.Limits = domain.Limits{}
		f.mustIngest(user, sourceEvent("s1", "doc.md", taskAccess()))
		private := sourceEvent("s2", "doc.md", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "T", AgentID: "A"})
		private.Spans[0].Parts[0].Text = "private revision"
		f.mustIngest(user, private)
		f.in.Limits = domain.Limits{MaxReferenceLinks: 1}
		r = f.mustIngest(user, userEvent("docs", "## References\n- doc.md\n", true))
		if links := len(f.references(semantic(r)[0].ID)); links != 1 || count(r) != 0 {
			t.Errorf("complete link set: links %d, truncation diagnostics %d; want 1, 0", links, count(r))
		}
	})
}

// pageRecorder records the page limits of reference lookups.
type pageRecorder struct {
	store.Store
	limits *[]int
}

func (s pageRecorder) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, sessionID, func(tx store.Tx) error { return fn(pageTx{tx, s.limits}) })
}

type pageTx struct {
	store.Tx
	limits *[]int
}

func (t pageTx) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	*t.limits = append(*t.limits, f.Page.Limit)
	return t.Tx.SourceItems(f)
}

// TestSourcePageSizedByBudget_DUR21: a source page never asks for more
// items than the remaining reference-link budget could use, plus one to
// detect truncation, instead of the full lookup limit.
func TestSourcePageSizedByBudget_DUR21(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		for i := range 3 {
			e := sourceEvent(fmt.Sprintf("s%d", i), "x.md", taskAccess())
			e.Spans[0].Parts[0].Text = fmt.Sprintf("x.md revision %d", i)
			f.mustIngest(user, e)
		}
		var pages []int
		f.s = pageRecorder{f.s, &pages}
		f.in.Limits = domain.Limits{MaxReferenceLinks: 5}
		// The first entry links 3 sources, leaving 2 of 5 for the second.
		f.mustIngest(user, userEvent("r", "## References\n- x.md\n- ./x.md\n", true))
		if !slices.Equal(pages, []int{6, 3}) {
			t.Errorf("page limits = %v, want [6 3] (remaining budget + 1)", pages)
		}
	})
}
