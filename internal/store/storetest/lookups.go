package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// docItem returns an item whose parts reference each blob in order.
func docItem(sess, id string, seq uint64, blobs ...domain.Blob) domain.ContextItem {
	it := NewItem(sess, id, seq, "caption "+id)
	for _, b := range blobs {
		it.Parts = append(it.Parts, domain.ContentPart{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: b.Hash, BlobSize: uint64(len(b.Data))})
	}
	it.ContentHash, it.SemanticBytes = domain.ContentHash(it.Parts), domain.SemanticBytes(it.Parts)
	return it
}

// testItemsByBlob checks the bounded lookup of items referencing a blob
// (R19, R5): each referencing item once, in (Seq, ID) order, with the
// transaction's own writes and never another session's.
func testItemsByBlob(t *testing.T, s store.Store) {
	x, y := NewBlob(sessA, []byte("blob x")), NewBlob(sessA, []byte("blob y"))
	update(t, s, sessB, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(NewBlob(sessB, x.Data)))
		return tx.InsertItem(docItem(sessB, "foreign", tx.NextSeq(), NewBlob(sessB, x.Data)))
	})
	var b, c, d domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(x))
		noErr(t, tx.InsertBlob(y))
		noErr(t, tx.InsertItem(NewItem(sessA, "text", tx.NextSeq(), "no blob")))
		seq := tx.NextSeq()
		c = docItem(sessA, "c", seq, x, y, x)
		b = docItem(sessA, "b", seq, x)
		noErr(t, tx.InsertItem(c))
		noErr(t, tx.InsertItem(b))
		got, err := tx.ItemsByBlob(x.Hash, 2)
		noErr(t, err)
		assertEqual(t, "own writes", got, []domain.ContextItem{b, c})
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(docItem(sessA, "rolled-back", tx.NextSeq(), x)))
		return errRollback
	})
	wantErr(t, err, errRollback)
	update(t, s, sessA, func(tx store.Tx) error {
		d = docItem(sessA, "a-later", tx.NextSeq(), y)
		return tx.InsertItem(d)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ItemsByBlob(x.Hash, 2)
		noErr(t, err)
		assertEqual(t, "blob x", got, []domain.ContextItem{b, c})
		got, err = tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		assertEqual(t, "blob y", got, []domain.ContextItem{c, d})
		got, err = tx.ItemsByBlob(domain.HashBytes([]byte("absent")), 1)
		noErr(t, err)
		assertEqual(t, "absent blob", got, []domain.ContextItem{})
		_, err = tx.ItemsByBlob(x.Hash, 1)
		wantErr(t, err, store.ErrLimitExceeded)
		_, err = tx.ItemsByBlob(x.Hash, 0)
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.ItemsByBlob("not-a-hash", 1)
		wantErr(t, err, domain.ErrInvalidRecord)
		got, err = tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		got[0].Parts[1].BlobHash = "scribbled"
		again, err := tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		assertEqual(t, "after mutating a result", again, []domain.ContextItem{c, d})
		return nil
	})
}

// dupFilter selects NewItem-shaped items with content hash of text.
func dupFilter(sess, text string, limit int) store.DuplicateFilter {
	it := NewItem(sess, "probe", 1, text)
	return store.DuplicateFilter{TaskID: it.TaskID, Section: it.Section, Role: it.Role, Authority: it.Authority,
		Access: it.Access, ContentHash: it.ContentHash, Limit: limit}
}

// testDuplicateCandidates checks the bounded duplicate-candidate lookup
// (R19, D10, FR-ING-005): items with exactly the given task, section, role,
// authority, access boundary, and content hash, in (Seq, ID) order. Any
// difference in one of them, or another session, is never a candidate.
func testDuplicateCandidates(t *testing.T, s store.Store) {
	update(t, s, sessB, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessB, "foreign", tx.NextSeq(), "same"))
	})
	var a, b domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		b = NewItem(sessA, "b", seq, "same")
		a = NewItem(sessA, "a", seq, "same")
		noErr(t, tx.InsertItem(b))
		noErr(t, tx.InsertItem(a))
		near := []func(it *domain.ContextItem){
			func(it *domain.ContextItem) { // other content
				it.Parts[0].Text = "other"
				it.ContentHash, it.SemanticBytes = domain.ContentHash(it.Parts), domain.SemanticBytes(it.Parts)
			},
			func(it *domain.ContextItem) { it.TaskID, it.Access.TaskID = "task2", "" },
			func(it *domain.ContextItem) { it.Authority = domain.AuthoritySystem },
			func(it *domain.ContextItem) { // narrower boundary, same scope family
				it.Scope, it.Access = domain.ScopeAgent, domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sessA, AgentID: "agent"}
			},
			func(it *domain.ContextItem) { it.DirectiveID, it.Section = "d", domain.SectionRemember },
			func(it *domain.ContextItem) {
				it.Role, it.Kind = domain.RoleTranscript, domain.KindUserMessage
			},
		}
		for i, mutate := range near {
			it := NewItem(sessA, "near-"+string(rune('0'+i)), tx.NextSeq(), "same")
			mutate(&it)
			noErr(t, tx.InsertItem(it))
		}
		got, err := tx.DuplicateCandidates(dupFilter(sessA, "same", 2))
		noErr(t, err)
		assertEqual(t, "own writes", got, []domain.ContextItem{a, b})
		return nil
	})
	var later domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		later = NewItem(sessA, "0-later", tx.NextSeq(), "same")
		return tx.InsertItem(later)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.DuplicateCandidates(dupFilter(sessA, "same", 3))
		noErr(t, err)
		assertEqual(t, "candidates", got, []domain.ContextItem{a, b, later})
		got, err = tx.DuplicateCandidates(dupFilter(sessA, "absent", 1))
		noErr(t, err)
		assertEqual(t, "absent", got, []domain.ContextItem{})
		_, err = tx.DuplicateCandidates(dupFilter(sessA, "same", 2))
		wantErr(t, err, store.ErrLimitExceeded)
		for name, mutate := range map[string]func(f *store.DuplicateFilter){
			"zero limit":    func(f *store.DuplicateFilter) { f.Limit = 0 },
			"bad hash":      func(f *store.DuplicateFilter) { f.ContentHash = "x" },
			"bad boundary":  func(f *store.DuplicateFilter) { f.Access = domain.AccessBoundary{} },
			"bad authority": func(f *store.DuplicateFilter) { f.Authority = "ROOT" },
			"bad section":   func(f *store.DuplicateFilter) { f.Section = "Bogus" },
			"bad role":      func(f *store.DuplicateFilter) { f.Role = "BOGUS" },
		} {
			f := dupFilter(sessA, "same", 3)
			mutate(&f)
			_, err := tx.DuplicateCandidates(f)
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("%s: error = %v, want ErrInvalidRecord", name, err)
			}
		}
		return nil
	})
}

// sourcedItem returns an item whose source is a locator of the given kind.
func sourcedItem(sess, id string, seq uint64, kind domain.SourceKind, locator string) domain.ContextItem {
	it := NewItem(sess, id, seq, "contents of "+id)
	it.Source = &domain.SourceRef{Kind: kind, Locator: locator}
	return it
}

// testItemsBySourceKey checks the bounded lookup of items by the rule-v1 key
// of their source locator (R19, M5, R2): lexically equal paths match, keys
// are exact bytes, non-locators are never indexed, and another session's
// items never appear.
func testItemsBySourceKey(t *testing.T, s store.Store) {
	update(t, s, sessB, func(tx store.Tx) error {
		return tx.InsertItem(sourcedItem(sessB, "foreign", tx.NextSeq(), domain.SourcePath, "docs/a.md"))
	})
	var a, b, u domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		b = sourcedItem(sessA, "b", seq, domain.SourcePath, "./docs//a.md")
		a = sourcedItem(sessA, "a", seq, domain.SourcePath, "docs/a.md")
		u = sourcedItem(sessA, "u", tx.NextSeq(), domain.SourceURL, "https://example.com/a.md")
		for _, it := range []domain.ContextItem{b, a, u,
			sourcedItem(sessA, "abs", tx.NextSeq(), domain.SourcePath, "/docs/a.md"),
			sourcedItem(sessA, "escape", tx.NextSeq(), domain.SourcePath, "../docs/a.md"),
			sourcedItem(sessA, "tool", tx.NextSeq(), domain.SourceTool, "docs/a.md"),
			NewItem(sessA, "unsourced", tx.NextSeq(), "x"),
		} {
			noErr(t, tx.InsertItem(it))
		}
		got, err := tx.ItemsBySourceKey("path:docs/a.md", 2)
		noErr(t, err)
		assertEqual(t, "own writes", got, []domain.ContextItem{a, b})
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(sourcedItem(sessA, "rolled-back", tx.NextSeq(), domain.SourcePath, "docs/a.md")))
		return errRollback
	})
	wantErr(t, err, errRollback)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ItemsBySourceKey("path:docs/a.md", 2)
		noErr(t, err)
		assertEqual(t, "path key", got, []domain.ContextItem{a, b})
		got, err = tx.ItemsBySourceKey("url:https://example.com/a.md", 1)
		noErr(t, err)
		assertEqual(t, "url key", got, []domain.ContextItem{u})
		for _, key := range []string{"path:/docs/a.md", "path:../docs/a.md", "path:docs/a.md/", "docs/a.md"} {
			got, err = tx.ItemsBySourceKey(key, 1)
			noErr(t, err)
			assertEqual(t, "key "+key, got, []domain.ContextItem{})
		}
		_, err = tx.ItemsBySourceKey("path:docs/a.md", 1)
		wantErr(t, err, store.ErrLimitExceeded)
		_, err = tx.ItemsBySourceKey("path:docs/a.md", 0)
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.ItemsBySourceKey("", 1)
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}
