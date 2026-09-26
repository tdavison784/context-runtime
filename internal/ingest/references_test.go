package ingest

import (
	"errors"
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

// TestReferenceDeclarationLookupBound_R19: declaration-time matching reads
// the bounded source-key index; more already-ingested sources for one
// locator than the lookup limit reject the declaring event (fail closed)
// rather than linking only some.
func TestReferenceDeclarationLookupBound_R19(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		for _, id := range []string{"t1", "t2", "t3"} {
			f.mustIngest(user, sourceEvent(id, "go.mod", taskAccess()))
		}
		e := userEvent("u1", "## References\n- go.mod\n", true)
		f.in.LookupLimit = 2
		before := f.lastSeq()
		if _, err := f.ingest(user, e); !errors.Is(err, store.ErrLimitExceeded) || f.lastSeq() != before {
			t.Errorf("over the bound: err = %v", err)
		}
		f.in.LookupLimit = 3
		r, err := f.ingest(user, e)
		if err != nil {
			t.Fatalf("within the bound: %v", err)
		}
		if got := f.references(semantic(r)[0].ID); len(got) != 3 {
			t.Errorf("linked %d earlier sources, want 3", len(got))
		}
	})
}

// TestReferenceLookupBound_R19: deferred linking reads the bounded
// locator-key index; more stored references to one locator than the lookup
// limit reject the source's event (fail closed) rather than linking only
// some.
func TestReferenceLookupBound_R19(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		f.mustIngest(user, userEvent("u1", "## References\n- go.mod\n- ./go.mod\n- a/../go.mod\n", true))
		f.in.LookupLimit = 2
		before := f.lastSeq()
		if _, err := f.ingest(user, sourceEvent("t1", "go.mod", taskAccess())); !errors.Is(err, store.ErrLimitExceeded) || f.lastSeq() != before {
			t.Errorf("over the bound: err = %v", err)
		}
		f.in.LookupLimit = 3
		if _, err := f.ingest(user, sourceEvent("t1", "go.mod", taskAccess())); err != nil {
			t.Errorf("within the bound: %v", err)
		}
	})
}
