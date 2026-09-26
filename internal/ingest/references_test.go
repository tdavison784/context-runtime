package ingest

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
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
	path := filepath.Join(t.TempDir(), "restart.db")
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
