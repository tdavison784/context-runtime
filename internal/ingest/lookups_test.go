package ingest

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// otherTask is a principal in another workflow and task of the session,
// whose records the fixtures' principal can never see.
func otherTask(a domain.Authority) domain.Principal {
	return domain.Principal{SessionID: sess, WorkflowID: "wf2", TaskID: "T2", AgentID: "Z", Authority: a}
}

func otherAccess() domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T2"}
}

// TestLookupsNeverLockOut_F1 is F1 (SEC-1.1, DUR-1.1): every ingest lookup
// is exact-key and filtered to what the source actor can access inside the
// store, before any limit, so neither another task padding an index key
// nor routine repetition within a task can reject ingestion or reveal
// hidden records. The lookup limit is set far below the padding.
func TestLookupsNeverLockOut_F1(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		other := otherTask(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		f.mustIngest(other, func() domain.Event {
			e := userEvent("o0", "hi", false)
			e.Spans[0].Access = otherAccess()
			return e
		}())
		const pad = 5
		f.in.LookupLimit = 2

		// Blob: another task references the same bytes many times; the
		// victim's own accessible referrer still authorizes its reference.
		img := []byte("shared image")
		part := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: img}
		f.mustIngest(user, domain.Event{EventID: "mine", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{part}}}})
		for i := range pad {
			f.mustIngest(other, domain.Event{EventID: fmt.Sprintf("ob%d", i), Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: otherAccess(), Parts: []domain.InputPart{part}}}})
		}
		ref := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", BlobHash: domain.HashBytes(img), BlobSize: uint64(len(img))}
		if _, err := f.ingest(user, domain.Event{EventID: "ref", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{ref}}}}); err != nil {
			t.Errorf("blob reference after padding: %v", err)
		}

		// Duplicates: repeating the same message past the limit never
		// fails; each repeat links to the one canonical occurrence.
		var first string
		for i := range pad {
			r := f.mustIngest(user, userEvent(fmt.Sprintf("ok%d", i), "ok", false))
			if i == 0 {
				first = r.Items[0].ID
			} else if len(r.Duplicates) != 1 || r.Duplicates[0].TargetID != first {
				t.Errorf("repeat %d duplicates = %v, want the first occurrence", i, r.Duplicates)
			}
		}

		// References: another task's sources and references under the
		// same locator never block the victim, and the victim links only
		// its own visible sources.
		for i := range pad {
			e := sourceEvent(fmt.Sprintf("os%d", i), "src/main.go", otherAccess())
			f.mustIngest(other, e)
			ex := userEvent(fmt.Sprintf("or%d", i), "## References\n- src/main.go\n", true)
			ex.Spans[0].Access = otherAccess()
			f.mustIngest(other, ex)
		}
		src := f.mustIngest(user, sourceEvent("s1", "src/main.go", taskAccess()))
		r := f.mustIngest(user, userEvent("r1", "## References\n- src/main.go\n", true))
		if got := f.references(semantic(r)[0].ID); len(got) != 1 || got[0] != src.Items[0].ID {
			t.Errorf("references = %v, want only the victim's source", got)
		}
		if _, err := f.ingest(user, sourceEvent("s2", "src/main.go", taskAccess())); err != nil {
			t.Errorf("source ingestion after padding: %v", err)
		}
	})
}

// unverifiedStore reports one extra unverified match from every duplicate
// candidate lookup, standing in for a legacy row whose content no longer
// verifies (which only a SQLite upgrade can produce).
type unverifiedStore struct{ store.Store }

func (s unverifiedStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, sessionID, func(tx store.Tx) error { return fn(unverifiedTx{tx}) })
}

type unverifiedTx struct{ store.Tx }

func (t unverifiedTx) CanonicalCandidates(f store.CanonicalFilter) (store.Lookup, error) {
	l, err := t.Tx.CanonicalCandidates(f)
	l.Unverified = append(l.Unverified, "itm_legacy")
	return l, err
}

// TestUnverifiedMatchesNeverBlock_DUR14: a lookup match that fails
// verification never blocks ingestion; it is reported as a content-free
// ItemUnverified diagnostic that names no item.
func TestUnverifiedMatchesNeverBlock_DUR14(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		f.s = unverifiedStore{f.s}
		r := f.mustIngest(principal(domain.AuthorityUser), userEvent("u1", "hello", false))
		n := 0
		for _, d := range r.Diagnostics {
			if d.Code == domain.ItemUnverified && d.Reason == domain.ReasonUnverifiedItem && d.DirectiveID == "" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("ItemUnverified diagnostics = %d, want 1: %+v", n, r.Diagnostics)
		}
	})
}

// repeatUnverifiedStore reports the same unverified legacy ID on every
// source page, as SQLite's lookahead could (DUR-3.1).
type repeatUnverifiedStore struct{ store.Store }

func (s repeatUnverifiedStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, sessionID, func(tx store.Tx) error { return fn(repeatUnverifiedTx{tx}) })
}

type repeatUnverifiedTx struct{ store.Tx }

func (t repeatUnverifiedTx) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	l, err := t.Tx.SourceItems(f)
	l.Unverified = append(l.Unverified, "itm_legacy")
	return l, err
}

// TestUnverifiedReportedOncePerEvent_DUR31: a legacy item reported on more
// than one lookup page yields one ItemUnverified diagnostic, not one per
// page, in the immutable receipt.
func TestUnverifiedReportedOncePerEvent_DUR31(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		for i := range 3 {
			e := sourceEvent(fmt.Sprintf("s%d", i), "p.md", taskAccess())
			e.Spans[0].Parts[0].Text = fmt.Sprintf("p.md revision %d", i)
			f.mustIngest(user, e)
		}
		f.s = repeatUnverifiedStore{f.s}
		f.in.LookupLimit = 1 // one source per page: three pages
		r := f.mustIngest(user, userEvent("r", "## References\n- p.md\n", true))
		n := 0
		for _, d := range r.Diagnostics {
			if d.Code == domain.ItemUnverified {
				n++
			}
		}
		if n != 1 {
			t.Errorf("ItemUnverified diagnostics = %d, want 1 for one legacy item", n)
		}
	})
}
