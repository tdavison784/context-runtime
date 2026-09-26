package ingest

import (
	"errors"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func derivedNotices(r domain.IngestReceipt) int {
	n := 0
	for _, d := range r.Diagnostics {
		if d.Code == domain.DirectiveIDDerived {
			n++
		}
	}
	return n
}

func residuals(r domain.IngestReceipt) []string {
	var out []string
	for _, it := range semantic(r) {
		if it.Kind == domain.KindInstruction && it.DirectiveID == "" {
			out = append(out, it.Parts[0].Text)
		}
	}
	return out
}

// TestR20_2_NoDerivedNoticeForRefusedItems: a DirectiveIDDerived notice is
// reported only for items ingestion actually wrote; members of a refused
// Working section (malformed, or with a boundary conflict) and dropped
// items get none.
func TestR20_2_NoDerivedNoticeForRefusedItems(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		r := f.mustIngest(user, userEvent("u1", "## Working\n- a\nstray prose\n- b\n", true))
		if n := derivedNotices(r); n != 0 {
			t.Errorf("malformed Working: %d derived-ID notices, want 0", n)
		}
		f.mustIngest(user, userEvent("u2", "## Pinned\n- [x] rule\n", true))
		r = f.mustIngest(user, userEvent("u3", "## Working\n- c\n- [x] {scope=TURN} d\n", true))
		if n := derivedNotices(r); n != 0 || len(semantic(r)) != 0 {
			t.Errorf("refused Working: %d notices, %d items", n, len(semantic(r)))
		}
		r = f.mustIngest(user, userEvent("u4", "## Working\n- e\n", true))
		if n := derivedNotices(r); n != 1 {
			t.Errorf("written member: %d notices, want 1", n)
		}
	})
}

// TestR20_3_Residuals: residual bytes are lossless; a residue of only
// whitespace and a leading BOM creates no item; text inside a malformed or
// refused section of a trusted span becomes residual instruction text, so
// a malformed trusted heading can never silently drop a requirement, while
// the text of items that were written is never duplicated into it.
func TestR20_3_Residuals(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, userEvent("u0", "hi", false))

		if r := f.mustIngest(sys, sysEvent("s1", "\xef\xbb\xbf## Remember\r\n- kept\r\n  more\r\n")); len(residuals(r)) != 0 {
			t.Errorf("BOM-only residue created %q", residuals(r))
		}
		if r := f.mustIngest(sys, sysEvent("s2", " \t\n## Remember\nx\n\n  \n")); len(residuals(r)) != 0 {
			t.Errorf("whitespace-only residue created %q", residuals(r))
		}

		text := "  Be concise.\n## Pinned\n- [a] Rule.\n"
		if r := f.mustIngest(sys, sysEvent("s3", text)); len(residuals(r)) != 1 || residuals(r)[0] != "  Be concise.\n" {
			t.Errorf("residual = %q, want the exact bytes", residuals(r))
		}

		r := f.mustIngest(sys, sysEvent("s4", "## Pinned\n- [ok] Keep this.\n- [bad id!] Never deploy on Fridays.\n"))
		if _, ok := byDirective(r, "ok"); !ok {
			t.Errorf("valid sibling dropped")
		}
		res := residuals(r)
		if len(res) != 1 || !strings.Contains(res[0], "Never deploy on Fridays.") || strings.Contains(res[0], "Keep this.") {
			t.Errorf("malformed-section residual = %q", res)
		}

		h := f.mustIngest(sys, domain.Event{EventID: "h1", Kind: domain.EventHarness, Spans: []domain.Span{
			textSpan(domain.AuthorityHarness, false, "## Working\n- a\nstray\n")}})
		if res := residuals(h); len(res) != 1 || res[0] != "## Working\n- a\nstray\n" {
			t.Errorf("refused Working residual = %q", res)
		}
	})
}

// TestR20_1_ErrorsNeverEchoItemIDs: ingestion errors carry only their
// sentinels, never an item (or other record) ID.
func TestR20_1_ErrorsNeverEchoItemIDs(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		p := principal(domain.AuthoritySystem)
		p.TaskID = ""
		sp := textSpan(domain.AuthoritySystem, false, "## Remember scope=SESSION ttl=2\nexpires\n")
		sp.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}
		_, err := f.ingest(p, domain.Event{EventID: "s1", Kind: domain.EventSystem, Spans: []domain.Span{sp}})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("err = %v, want ErrInvalidRecord", err)
		}
		if strings.Contains(err.Error(), "itm_") || strings.Contains(err.Error(), "item ") {
			t.Errorf("error echoes an item: %q", err)
		}
	})
}
