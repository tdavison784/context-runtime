package memory

import (
	"cmp"
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
	seen := map[string]bool{}
	for _, p := range it.Parts {
		if p.BlobHash != "" && !seen[p.BlobHash] {
			seen[p.BlobHash] = true
			t.blobOwners.add(blobKey{p.BlobHash, ownersOf(it.Access)}, it.ID)
		}
	}
	t.canonical.add(canonicalKeyOf(it), it.ID)
	if it.Section == domain.SectionWorking {
		t.working.add(workingKey{it.TaskID, it.Authority, it.Access}, it.ID)
	}
	if k, ok := sourceKeyOf(it); ok {
		t.sources.add(k, it.ID)
	}
}

// retireLookups drops an item that stopped being live from the live
// indexes.
func (t *tx) retireLookups(id string) {
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
}

func sourceKeyOf(it domain.ContextItem) (sourceKey, bool) {
	if it.Source == nil {
		return sourceKey{}, false
	}
	key, ok := domain.LocatorKey(it.Source.Kind, it.Source.Locator)
	return sourceKey{key, ownersOf(it.Access)}, ok
}

// sortedItems loads ids, keeps those whose boundary permits viewer, and
// orders them by (Seq, ID).
func (r *readTx) sortedItems(ids []string, viewer domain.Principal) []domain.ContextItem {
	out := make([]domain.ContextItem, 0, len(ids))
	for _, id := range ids {
		if it, ok := r.items.get(id); ok && it.Access.Permits(viewer) {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func (r *readTx) BlobReferrer(f store.BlobReferrerFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	var ids []string
	for _, o := range permitted(f.Viewer) {
		for id := range r.blobOwners.lookup(blobKey{f.BlobHash, o}) {
			ids = append(ids, id)
		}
	}
	for _, it := range r.sortedItems(ids, f.Viewer) {
		if f.Within.Within(it.Access) {
			return store.Lookup{Items: []domain.ContextItem{it}}, nil
		}
	}
	return store.Lookup{Items: []domain.ContextItem{}}, nil
}

func (r *readTx) CanonicalCandidates(f store.CanonicalFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	out := store.Lookup{Items: []domain.ContextItem{}}
	if !f.Access.Permits(f.Viewer) {
		return out, nil
	}
	key := canonicalKey{f.TaskID, f.Section, f.DirectiveID, f.Kind, f.Role, f.Authority, f.Access, f.ContentHash}
	return r.boundedLive(r.canonical.lookup(key), f.Viewer, f.Limit, nil)
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
	return r.boundedLive(r.working.lookup(workingKey{f.TaskID, f.Authority, f.Access}), f.Viewer, f.Limit, named)
}

// boundedLive loads the IDs, keeps visible items that pass keep (if set),
// and fails with store.ErrLimitExceeded past limit.
func (r *readTx) boundedLive(ids func(func(string) bool), viewer domain.Principal, limit int, keep func(domain.ContextItem) bool) (store.Lookup, error) {
	var all []string
	for id := range ids {
		all = append(all, id)
	}
	out := store.Lookup{Items: []domain.ContextItem{}}
	for _, it := range r.sortedItems(all, viewer) {
		if keep != nil && !keep(it) {
			continue
		}
		if len(out.Items) == limit {
			return store.Lookup{}, store.ErrLimitExceeded
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

func (r *readTx) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	if err := r.check(); err != nil {
		return store.Lookup{}, err
	}
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	var ids []string
	for _, o := range permitted(f.Viewer) {
		for id := range r.sources.lookup(sourceKey{f.LocatorKey, o}) {
			ids = append(ids, id)
		}
	}
	out := store.Lookup{Items: []domain.ContextItem{}}
	for _, it := range r.sortedItems(ids, f.Viewer) {
		if !after(it.Seq, it.ID, f.Page.After) {
			continue
		}
		if len(out.Items) == f.Page.Limit {
			out.More = true
			break
		}
		out.Items = append(out.Items, it)
		out.Next = store.Cursor{Seq: it.Seq, ID: it.ID}
	}
	if len(out.Items) == 0 {
		out.Next = f.Page.After
	}
	return out, nil
}

func (r *readTx) VisibleReferences(f store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	if err := r.check(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	if err := f.Validate(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	var refs []domain.UnresolvedReference
	for _, o := range permitted(f.Viewer) {
		for id := range r.refOwners.lookup(sourceKey{f.LocatorKey, o}) {
			if v, ok := r.references.get(id); ok && v.RuleVersion == domain.LocatorRuleVersion && v.Access.Permits(f.Viewer) {
				refs = append(refs, v)
			}
		}
	}
	slices.SortFunc(refs, func(a, b domain.UnresolvedReference) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	out, next := []domain.UnresolvedReference{}, f.Page.After
	for _, v := range refs {
		if !after(v.Seq, v.ID, f.Page.After) {
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

// after reports whether (seq, id) is strictly after c.
func after(seq uint64, id string, c store.Cursor) bool {
	return seq > c.Seq || seq == c.Seq && id > c.ID
}
