package storetest

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Principals and boundaries for the access-filtered lookup tests (F1).
var (
	viewerT = NewPrincipal(sessA, domain.AuthorityUser) // task "task", agent "agent"
)

func otherTaskViewer() domain.Principal {
	p := viewerT
	p.TaskID = "task2"
	return p
}

// task2Boundary is a TASK boundary no viewerT-shaped principal can see.
func task2Boundary() domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task2"}
}

// inTask2 moves an item into task2's boundary.
func inTask2(it domain.ContextItem) domain.ContextItem {
	it.TaskID, it.Scope, it.Access = "task2", domain.ScopeTask, task2Boundary()
	return it
}

func ids(items []domain.ContextItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// testBlobReferrerAccess checks blob authorization's lookup (F1, SEC-1.1,
// DUR-1.1, D19): only referrers the viewer can see count, however many
// hidden ones share the hash, and the referrer must contain Within.
func testBlobReferrerAccess(t *testing.T, s store.Store) {
	x := NewBlob(sessA, []byte("shared bytes"))
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(x))
		for i := range 20 { // padding from another task, earlier than ours
			noErr(t, tx.InsertItem(inTask2(docItem(sessA, fmt.Sprintf("hidden-%02d", i), tx.NextSeq(), x))))
		}
		mine := docItem(sessA, "mine", tx.NextSeq(), x)
		mine.Scope, mine.Access = domain.ScopeTask, DirectiveBoundary(sessA)
		noErr(t, tx.InsertItem(mine))
		for i := range 3 { // more of our own referrers
			it := docItem(sessA, fmt.Sprintf("mine-%d", i), tx.NextSeq(), x)
			it.Scope, it.Access = domain.ScopeTask, DirectiveBoundary(sessA)
			noErr(t, tx.InsertItem(it))
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		ref := func(viewer domain.Principal, hash string, within domain.AccessBoundary) []string {
			t.Helper()
			l, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: viewer, BlobHash: hash, Within: within})
			noErr(t, err)
			return ids(l.Items)
		}
		assertEqual(t, "task viewer", ref(viewerT, x.Hash, DirectiveBoundary(sessA)), []string{"mine"})
		assertEqual(t, "narrower within", ref(viewerT, x.Hash, PrivateBoundary(sessA)), []string{"mine"})
		// A SESSION-wide item is not within a TASK referrer.
		assertEqual(t, "wider within", ref(viewerT, x.Hash, domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}), []string{})
		assertEqual(t, "other task viewer", ref(otherTaskViewer(), x.Hash, task2Boundary()), []string{"hidden-00"})
		assertEqual(t, "missing blob", ref(viewerT, domain.HashBytes([]byte("absent")), DirectiveBoundary(sessA)), []string{})
		stranger := viewerT
		stranger.TaskID = "task3"
		assertEqual(t, "stranger", ref(stranger, x.Hash, domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task3"}), []string{})
		_, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: viewerT, BlobHash: "x", Within: DirectiveBoundary(sessA)})
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.BlobReferrer(store.BlobReferrerFilter{BlobHash: x.Hash, Within: DirectiveBoundary(sessA)})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

func canonicalFilter(it domain.ContextItem, limit int) store.CanonicalFilter {
	return store.CanonicalFilter{Viewer: viewerT, TaskID: it.TaskID, Section: it.Section, DirectiveID: it.DirectiveID, Kind: it.Kind,
		Role: it.Role, Authority: it.Authority, Access: it.Access, ContentHash: it.ContentHash, Limit: limit}
}

// testCanonicalCandidates checks the live duplicate-candidate lookup (F1,
// DUR-1.1, SEC-1.1, D10): duplicates and superseded versions leave the set,
// so any number of identical messages keeps one candidate; other
// partitions and hidden items never count.
func testCanonicalCandidates(t *testing.T, s store.Store) {
	probe := NewItem(sessA, "probe", 1, "ok")
	update(t, s, sessA, func(tx store.Tx) error {
		for i := range 10 { // hidden padding with the same bytes
			noErr(t, tx.InsertItem(inTask2(NewItem(sessA, fmt.Sprintf("hidden-%d", i), tx.NextSeq(), "ok"))))
		}
		noErr(t, tx.InsertItem(NewItem(sessA, "canonical", tx.NextSeq(), "ok")))
		for i := range 10 { // routine repeats, each classified a duplicate
			id := fmt.Sprintf("dup-%d", i)
			noErr(t, tx.InsertItem(NewItem(sessA, id, tx.NextSeq(), "ok")))
			noErr(t, tx.InsertRelationship(NewRelationship(sessA, "edge-"+id, domain.RelDuplicateOf, id, "canonical", tx.NextSeq())))
		}
		l, err := tx.CanonicalCandidates(canonicalFilter(probe, 1))
		noErr(t, err)
		assertEqual(t, "inside Update", ids(l.Items), []string{"canonical"})
		return nil
	})
	// A retirement that rolls back leaves the item live.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "late", tx.NextSeq(), "ok")))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "edge-canonical", domain.RelDuplicateOf, "canonical", "late", tx.NextSeq())))
		l, err := tx.CanonicalCandidates(canonicalFilter(probe, 1))
		noErr(t, err)
		assertEqual(t, "after classifying canonical", ids(l.Items), []string{"late"})
		return errRollback
	})
	wantErr(t, err, errRollback)
	view(t, s, sessA, func(tx store.ReadTx) error {
		l, err := tx.CanonicalCandidates(canonicalFilter(probe, 1))
		noErr(t, err)
		assertEqual(t, "after rollback", ids(l.Items), []string{"canonical"})
		other := canonicalFilter(inTask2(probe), 1)
		l, err = tx.CanonicalCandidates(other)
		noErr(t, err)
		assertEqual(t, "partition the viewer cannot see", ids(l.Items), []string{})
		other.Viewer = otherTaskViewer()
		_, err = tx.CanonicalCandidates(other)
		wantErr(t, err, store.ErrLimitExceeded) // ten live, visible, same-partition items
		for name, mutate := range map[string]func(f *store.CanonicalFilter){
			"directive ID": func(f *store.CanonicalFilter) { f.DirectiveID = "d" },
			"kind":         func(f *store.CanonicalFilter) { f.Kind = domain.KindEvidence },
			"role":         func(f *store.CanonicalFilter) { f.Role = domain.RoleTranscript },
			"authority":    func(f *store.CanonicalFilter) { f.Authority = domain.AuthoritySystem },
			"section":      func(f *store.CanonicalFilter) { f.Section = domain.SectionRemember },
		} {
			f := canonicalFilter(probe, 1)
			mutate(&f)
			l, err := tx.CanonicalCandidates(f)
			noErr(t, err)
			assertEqual(t, "other "+name, ids(l.Items), []string{})
		}
		f := canonicalFilter(probe, 1)
		f.Viewer = domain.Principal{}
		_, err = tx.CanonicalCandidates(f)
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// workingItem returns a Working member with directive ID id in the task
// boundary.
func workingItem(sess, id string, seq uint64, text string) domain.ContextItem {
	it := NewDirective(sess, id, id, seq, text)
	it.Section, it.Kind, it.Generation = domain.SectionWorking, domain.KindTaskState, domain.GenerationWorking
	return it
}

func workingFilter(viewer domain.Principal, access domain.AccessBoundary, limit int) store.WorkingFilter {
	return store.WorkingFilter{Viewer: viewer, TaskID: access.TaskID, Authority: domain.AuthorityUser, Access: access, Limit: limit}
}

// testCurrentWorking checks the Working snapshot lookup (F1, SEC-1.2, D11):
// the partition's live, mapped Working items only, never other
// partitions or retired, unfiled, or hidden members.
func testCurrentWorking(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		for _, id := range []string{"w0", "w1", "w2"} {
			noErr(t, tx.InsertItem(workingItem(sessA, id, tx.NextSeq(), id)))
			noErr(t, tx.SetCurrentVersion(id))
		}
		// w0 replaced by w0b; the old version is retired.
		noErr(t, tx.InsertItem(workingItem(sessA, "w0b", tx.NextSeq(), "w0 v2")))
		wb, err := tx.Item("w0b")
		noErr(t, err)
		_ = wb
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "w0b-w0", domain.RelSupersedes, "w0b", "w0", tx.NextSeq())))
		noErr(t, tx.SetCurrentVersion("w0b"))
		// Inserted but never filed: not a version.
		noErr(t, tx.InsertItem(workingItem(sessA, "unfiled", tx.NextSeq(), "x")))
		// Other partitions: another authority and another task.
		sys := workingItem(sessA, "sys", tx.NextSeq(), "sys")
		sys.Authority = domain.AuthoritySystem
		noErr(t, tx.InsertItem(sys))
		noErr(t, tx.SetCurrentVersion("sys"))
		hidden := inTask2(workingItem(sessA, "hidden", tx.NextSeq(), "hidden"))
		noErr(t, tx.InsertItem(hidden))
		noErr(t, tx.SetCurrentVersion("hidden"))
		// A non-Working directive in the same partition.
		noErr(t, tx.InsertItem(NewDirective(sessA, "pin", "pin", tx.NextSeq(), "pinned")))
		noErr(t, tx.SetCurrentVersion("pin"))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		l, err := tx.CurrentWorking(workingFilter(viewerT, DirectiveBoundary(sessA), 3))
		noErr(t, err)
		assertEqual(t, "current Working", ids(l.Items), []string{"w1", "w2", "w0b"})
		_, err = tx.CurrentWorking(workingFilter(viewerT, DirectiveBoundary(sessA), 2))
		wantErr(t, err, store.ErrLimitExceeded)
		l, err = tx.CurrentWorking(workingFilter(viewerT, task2Boundary(), 1))
		noErr(t, err)
		assertEqual(t, "partition the viewer cannot see", ids(l.Items), []string{})
		l, err = tx.CurrentWorking(workingFilter(otherTaskViewer(), task2Boundary(), 1))
		noErr(t, err)
		assertEqual(t, "other task", ids(l.Items), []string{"hidden"})
		f := workingFilter(viewerT, DirectiveBoundary(sessA), 0)
		_, err = tx.CurrentWorking(f)
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// testSourceItems checks the paged source-key lookup (F1, SEC-1.1,
// DUR-1.1, M5): hidden items never appear or count, duplicates leave the
// set, and paging is complete and never fails.
func testSourceItems(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		for i := range 10 {
			noErr(t, tx.InsertItem(inTask2(sourcedItem(sessA, fmt.Sprintf("hidden-%d", i), tx.NextSeq(), domain.SourcePath, "src/main.go"))))
		}
		for i := range 5 {
			noErr(t, tx.InsertItem(sourcedItem(sessA, fmt.Sprintf("read-%d", i), tx.NextSeq(), domain.SourcePath, "./src//main.go")))
		}
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "dup", domain.RelDuplicateOf, "read-4", "read-0", tx.NextSeq())))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		var got []string
		f := store.SourceFilter{Viewer: viewerT, LocatorKey: "path:src/main.go", Page: store.Page{Limit: 2}}
		for pages := 0; ; pages++ {
			l, err := tx.SourceItems(f)
			noErr(t, err)
			got = append(got, ids(l.Items)...)
			if !l.More {
				break
			}
			if pages > 5 {
				t.Fatal("paging does not terminate")
			}
			f.Page.After = l.Next
		}
		assertEqual(t, "all visible live sources", got, []string{"read-0", "read-1", "read-2", "read-3"})
		l, err := tx.SourceItems(store.SourceFilter{Viewer: otherTaskViewer(), LocatorKey: "path:src/main.go", Page: store.Page{Limit: 20}})
		noErr(t, err)
		// The session-wide reads are visible to task2 as well.
		if len(l.Items) != 14 || l.More {
			t.Errorf("other task sees %d sources (more=%v), want its 10 plus the 4 session-wide", len(l.Items), l.More)
		}
		_, err = tx.SourceItems(store.SourceFilter{Viewer: viewerT, LocatorKey: "path:src/main.go"})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// testVisibleReferences checks the paged reference lookup (F1, SEC-1.1,
// M5, R2): references whose boundary does not permit the viewer are never
// returned or counted.
func testVisibleReferences(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "ref-item", tx.NextSeq(), "refs")))
		for i := range 10 {
			r := NewUnresolvedReference(sessA, occ, i, "ref-item", "path:src/main.go", tx.NextSeq())
			r.Access, r.RuleVersion = task2Boundary(), domain.LocatorRuleVersion
			noErr(t, tx.InsertUnresolvedReference(r))
		}
		for i := 10; i < 13; i++ {
			r := NewUnresolvedReference(sessA, occ, i, "ref-item", "path:src/main.go", tx.NextSeq())
			r.Access, r.RuleVersion = DirectiveBoundary(sessA), domain.LocatorRuleVersion
			noErr(t, tx.InsertUnresolvedReference(r))
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		f := store.VisibleReferenceFilter{Viewer: viewerT, LocatorKey: "path:src/main.go", Page: store.Page{Limit: 2}}
		refs, more, next, err := tx.VisibleReferences(f)
		noErr(t, err)
		if len(refs) != 2 || !more || refs[0].Ordinal != 10 {
			t.Fatalf("first page = %d refs, more=%v", len(refs), more)
		}
		f.Page.After = next
		refs, more, _, err = tx.VisibleReferences(f)
		noErr(t, err)
		if len(refs) != 1 || more || refs[0].Ordinal != 12 {
			t.Errorf("second page = %+v, more=%v", refs, more)
		}
		refs, _, _, err = tx.VisibleReferences(store.VisibleReferenceFilter{Viewer: viewerT, LocatorKey: "path:other", Page: store.Page{Limit: 1}})
		noErr(t, err)
		if len(refs) != 0 {
			t.Errorf("other key = %d refs", len(refs))
		}
		return nil
	})
}

// testSourceItemsMixedOwners checks that a lookup paged across several
// owner combinations the viewer can see returns one merged (Seq, ID) order
// with no duplicates or skips, at every page size (SPEC-4.3): items of the
// session, workflow, task, agent, and task+agent boundaries are interleaved
// with hidden ones, so serving the combinations one after another fails.
func testSourceItemsMixedOwners(t *testing.T, s store.Store) {
	owners := []domain.AccessBoundary{
		{Scope: domain.ScopeSession, SessionID: sessA},
		{Scope: domain.ScopeWorkflow, SessionID: sessA, WorkflowID: "wf"},
		{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task"},
		{Scope: domain.ScopeAgent, SessionID: sessA, AgentID: "agent"},
		{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task", AgentID: "agent"},
	}
	var want []string
	update(t, s, sessA, func(tx store.Tx) error {
		for i := range 15 {
			it := sourcedItem(sessA, fmt.Sprintf("mixed-%02d", i), tx.NextSeq(), domain.SourcePath, "mixed.go")
			it.Access = owners[i%len(owners)]
			it.Scope = it.Access.Scope
			noErr(t, tx.InsertItem(it))
			want = append(want, it.ID)
			noErr(t, tx.InsertItem(inTask2(sourcedItem(sessA, fmt.Sprintf("hidden-mixed-%02d", i), tx.NextSeq(), domain.SourcePath, "mixed.go"))))
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		for limit := 1; limit <= 4; limit++ {
			var got []string
			f := store.SourceFilter{Viewer: viewerT, LocatorKey: "path:mixed.go", Page: store.Page{Limit: limit}}
			for pages := 0; ; pages++ {
				l, err := tx.SourceItems(f)
				noErr(t, err)
				got = append(got, ids(l.Items)...)
				if !l.More {
					break
				}
				if pages > 20 {
					t.Fatal("paging does not terminate")
				}
				f.Page.After = l.Next
			}
			assertEqual(t, fmt.Sprintf("mixed-owner pages of %d", limit), got, want)
		}
		return nil
	})
}
