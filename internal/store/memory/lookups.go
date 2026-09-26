package memory

import (
	"iter"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// owners is the owner-column part of an access boundary; lookups filter by
// it so domain.AccessBoundary.Permits becomes a handful of equality probes.
type owners struct{ workflowID, taskID, agentID string }

func ownersOf(b domain.AccessBoundary) owners { return owners{b.WorkflowID, b.TaskID, b.AgentID} }

// permitted lists every owners value whose boundary could permit p.
func permitted(p domain.Principal) []owners {
	wfs, tasks, agents := store.PermittedOwners(p)
	var out []owners
	for _, w := range wfs {
		for _, t := range tasks {
			for _, a := range agents {
				out = append(out, owners{w, t, a})
			}
		}
	}
	return out
}

type blobKey struct {
	hash string
	owners
}

type canonicalKey struct {
	taskID      string
	section     domain.DirectiveSection
	directiveID string
	kind        domain.Kind
	role        domain.ItemRole
	authority   domain.Authority
	access      domain.AccessBoundary
	contentHash string
}

func canonicalKeyOf(it domain.ContextItem) canonicalKey {
	return canonicalKey{it.TaskID, it.Section, it.DirectiveID, it.Kind, it.Role, it.Authority, it.Access, it.ContentHash}
}

type workingKey struct {
	taskID    string
	authority domain.Authority
	access    domain.AccessBoundary
}

type sourceKey struct {
	key string
	owners
}

// indexLookups adds a new item to the access-filtered lookup indexes.
func (t *tx) indexLookups(it domain.ContextItem) {
	ref := seqRef{it.Seq, it.ID}
	for _, k := range blobKeysOf(it) {
		t.blobOwners.add(k, ref)
	}
	t.canonical.add(canonicalKeyOf(it), ref)
	if it.Section == domain.SectionWorking {
		t.working.add(workingKey{it.TaskID, it.Authority, it.Access}, ref)
	}
	if k, ok := sourceKeyOf(it); ok {
		t.sources.add(k, ref)
	}
}

func blobKeysOf(it domain.ContextItem) []blobKey {
	var out []blobKey
	seen := map[string]bool{}
	for _, p := range it.Parts {
		if p.BlobHash != "" && !seen[p.BlobHash] {
			seen[p.BlobHash] = true
			out = append(out, blobKey{p.BlobHash, ownersOf(it.Access)})
		}
	}
	return out
}

// retireLookups drops an item that stopped being live from the live
// indexes. A duplicate also leaves the blob index (DUR-2.1): its canonical
// item has the same content and boundary, so it authorizes every
// reference the duplicate could.
func (t *tx) retireLookups(id string, duplicate bool) {
	it, ok := t.items.peek(id)
	if !ok {
		return
	}
	t.canonical.remove(canonicalKeyOf(it), id)
	if it.Section == domain.SectionWorking {
		t.working.remove(workingKey{it.TaskID, it.Authority, it.Access}, id)
	}
	if k, ok := sourceKeyOf(it); ok {
		t.sources.remove(k, id)
	}
	if duplicate {
		for _, k := range blobKeysOf(it) {
			t.blobOwners.remove(k, id)
		}
	}
}

func sourceKeyOf(it domain.ContextItem) (sourceKey, bool) {
	if it.Source == nil {
		return sourceKey{}, false
	}
	key, ok := domain.LocatorKey(it.Source.Kind, it.Source.Locator)
	return sourceKey{key, ownersOf(it.Access)}, ok
}

// intersectOwners lists the owners values whose boundary permits every
// principal in ps.
func intersectOwners(ps ...domain.Principal) []owners {
	var out []owners
	for _, o := range permitted(ps[0]) {
		ok := true
		for _, p := range ps[1:] {
			ok = ok && slices.Contains(permitted(p), o)
		}
		if ok {
			out = append(out, o)
		}
	}
	return out
}

// scan walks refs in order, loading each item once, and collects visible
// items until want are found (found=true). Work is bounded by what it
// returns plus the entries it skips (DUR-2.1).
func (r *readTx) scan(refs iter.Seq[seqRef], viewer domain.Principal, want int, keep func(domain.ContextItem) bool) (store.Lookup, bool) {
	out := store.Lookup{Items: []domain.ContextItem{}}
	for ref := range refs {
		it, ok := r.items.get(ref.id)
		if !ok || !it.Access.Permits(viewer) || keep != nil && !keep(it) {
			continue
		}
		out.Items = append(out.Items, it)
		if len(out.Items) == want {
			return out, true
		}
		out.Next = store.Cursor{Seq: ref.seq, ID: ref.id}
	}
	return out, false
}

func (r *readTx) BlobReferrer(f store.BlobReferrerFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if f.Within.SessionID != r.sessionID {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	within := domain.Principal{SessionID: f.Within.SessionID, WorkflowID: f.Within.WorkflowID, TaskID: f.Within.TaskID, AgentID: f.Within.AgentID}
	var keys []blobKey
	for _, o := range intersectOwners(f.Viewer, within) {
		keys = append(keys, blobKey{f.BlobHash, o})
	}
	l, _ := r.scan(mergeAfter(&r.blobOwners, keys, seqRef{}), f.Viewer, 1, func(it domain.ContextItem) bool { return f.Within.Within(it.Access) })
	l.Next = store.Cursor{}
	return l, nil
}

func (r *readTx) CanonicalCandidates(f store.CanonicalFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	key := canonicalKey{f.TaskID, f.Section, f.DirectiveID, f.Kind, f.Role, f.Authority, f.Access, f.ContentHash}
	l, over := r.scan(r.canonical.after(key, seqRef{}), f.Viewer, f.Limit+1, nil)
	if over {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, nil
}

func (r *readTx) CurrentWorking(f store.WorkingFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	named := func(it domain.ContextItem) bool {
		k, ok := it.CurrentKey()
		if !ok {
			return false
		}
		id, ok := r.directives.get(directiveKey{k.TaskID, k.ID, k.Access, k.Namespace})
		return ok && id == it.ID
	}
	l, over := r.scan(r.working.after(workingKey{f.TaskID, f.Authority, f.Access}, seqRef{}), f.Viewer, f.Limit+1, named)
	if over {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, nil
}

func (r *readTx) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	var keys []sourceKey
	for _, o := range permitted(f.Viewer) {
		keys = append(keys, sourceKey{f.LocatorKey, o})
	}
	after := seqRef{f.Page.After.Seq, f.Page.After.ID}
	l, more := r.scan(mergeAfter(&r.sources, keys, after), f.Viewer, f.Page.Limit+1, nil)
	if more { // the extra item only proves more remain
		l.Items, l.More = l.Items[:f.Page.Limit], true
	}
	if len(l.Items) == 0 {
		l.Next = f.Page.After
	}
	return l, nil
}

func (r *readTx) VisibleReferences(f store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	if err := r.check(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	if err := f.Validate(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	var keys []sourceKey
	for _, o := range permitted(f.Viewer) {
		keys = append(keys, sourceKey{f.LocatorKey, o})
	}
	out, next := []domain.UnresolvedReference{}, f.Page.After
	for ref := range mergeAfter(&r.refOwners, keys, seqRef{f.Page.After.Seq, f.Page.After.ID}) {
		v, ok := r.references.get(ref.id)
		if !ok || v.RuleVersion != domain.LocatorRuleVersion || !v.Access.Permits(f.Viewer) {
			continue
		}
		if len(out) == f.Page.Limit {
			return out, true, next, nil
		}
		out = append(out, v)
		next = store.Cursor{Seq: v.Seq, ID: v.ID}
	}
	return out, false, next, nil
}
