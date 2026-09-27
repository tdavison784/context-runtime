package memory

import (
	"iter"
	"slices"
	"sort"
)

// index is an append-only multimap from a key to record IDs, seen through a
// transaction like a table. Relationships are immutable and never deleted,
// so an index only grows: a transaction appends to its overlay and commit
// appends the overlay to the committed lists.
type index[K comparable] struct {
	base map[K][]string
	over map[K][]string // nil in a read-only transaction
	// yields counts IDs lookup has yielded, so tests can assert a keyed
	// read walks only its key (SPEC-3.2).
	yields int
}

func newIndex[K comparable](base map[K][]string, writable bool) index[K] {
	x := index[K]{base: base}
	if writable {
		x.over = map[K][]string{}
	}
	return x
}

func (x *index[K]) add(k K, id string) { x.over[k] = append(x.over[k], id) }

// lookup yields the IDs under k, committed ones first.
func (x *index[K]) lookup(k K) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, ids := range [][]string{x.base[k], x.over[k]} {
			for _, id := range ids {
				x.yields++
				if !yield(id) {
					return
				}
			}
		}
	}
}

func (x *index[K]) commit() {
	for k, ids := range x.over {
		x.base[k] = append(x.base[k], ids...)
	}
}

// iterFirst returns the first value of seq, if any.
func iterFirst[V any](seq iter.Seq[V]) (V, bool) {
	for v := range seq {
		return v, true
	}
	var zero V
	return zero, false
}

// liveIndex is a multimap from a key to a set of values (usually the IDs of
// live items), seen through a transaction. Unlike index it supports
// removal (an item stops being live when it is superseded or classified a
// duplicate): the overlay records additions and tombstones, and commit
// applies both.
type liveIndex[K, V comparable] struct {
	base map[K]map[V]bool
	over map[K]map[V]bool // true adds, false removes; nil when read-only
}

func newLiveIndex[K, V comparable](base map[K]map[V]bool, writable bool) liveIndex[K, V] {
	x := liveIndex[K, V]{base: base}
	if writable {
		x.over = map[K]map[V]bool{}
	}
	return x
}

func (x *liveIndex[K, V]) set(k K, v V, live bool) {
	m := x.over[k]
	if m == nil {
		m = map[V]bool{}
		x.over[k] = m
	}
	m[v] = live
}

func (x *liveIndex[K, V]) add(k K, v V)    { x.set(k, v, true) }
func (x *liveIndex[K, V]) remove(k K, v V) { x.set(k, v, false) }

// lookup yields the live values under k in no particular order.
func (x *liveIndex[K, V]) lookup(k K) iter.Seq[V] {
	return func(yield func(V) bool) {
		over := x.over[k]
		for v := range x.base[k] {
			if live, ok := over[v]; ok && !live {
				continue
			}
			if !yield(v) {
				return
			}
		}
		for v, live := range over {
			if live && !x.base[k][v] {
				if !yield(v) {
					return
				}
			}
		}
	}
}

func (x *liveIndex[K, V]) commit() {
	for k, m := range x.over {
		for v, live := range m {
			if live {
				if x.base[k] == nil {
					x.base[k] = map[V]bool{}
				}
				x.base[k][v] = true
			} else {
				delete(x.base[k], v)
			}
		}
	}
}

// seqRef is an index entry in (Seq, ID) order.
type seqRef struct {
	seq uint64
	id  string
}

func (a seqRef) less(b seqRef) bool { return a.seq < b.seq || a.seq == b.seq && a.id < b.id }

// orderedIndex maps a key to entries kept in (Seq, ID) order, seen through a
// transaction, so a lookup can start at a cursor and stop after a few
// entries instead of collecting and sorting every match (DUR-2.1). The
// overlay holds sorted additions and removals; commit merges both into the
// committed lists in place, doing work proportional to the change
// (SPEC-3.1 item 3).
type orderedIndex[K comparable] struct {
	base map[K][]seqRef
	over map[K][]seqRef        // sorted additions; nil when read-only
	gone map[K]map[seqRef]bool // removals in this transaction, by exact entry
	// commitWork counts additions and removals commit applied, so tests
	// can assert it never rebuilds a list.
	commitWork int
}

func newOrderedIndex[K comparable](base map[K][]seqRef, writable bool) orderedIndex[K] {
	x := orderedIndex[K]{base: base}
	if writable {
		x.over, x.gone = map[K][]seqRef{}, map[K]map[seqRef]bool{}
	}
	return x
}

// search returns the position of r in l, or where it would be inserted.
func search(l []seqRef, r seqRef) int {
	return sort.Search(len(l), func(i int) bool { return !l[i].less(r) })
}

func (x *orderedIndex[K]) add(k K, r seqRef) {
	l := x.over[k]
	x.over[k] = slices.Insert(l, search(l, r), r)
	delete(x.gone[k], r)
}

// remove drops exactly entry r (its Seq locates it) from k; another entry
// with the same ID, such as a newer version's, is unaffected.
func (x *orderedIndex[K]) remove(k K, r seqRef) {
	if x.gone[k] == nil {
		x.gone[k] = map[seqRef]bool{}
	}
	x.gone[k][r] = true
}

// after yields k's entries strictly after c, in order.
func (x *orderedIndex[K]) after(k K, c seqRef) iter.Seq[seqRef] {
	return func(yield func(seqRef) bool) {
		b, o, gone := x.base[k], x.over[k], x.gone[k]
		start := func(l []seqRef) int { return sort.Search(len(l), func(i int) bool { return c.less(l[i]) }) }
		i, j := start(b), start(o)
		for i < len(b) || j < len(o) {
			var r seqRef
			if j == len(o) || i < len(b) && b[i].less(o[j]) {
				r, i = b[i], i+1
			} else {
				r, j = o[j], j+1
			}
			if gone[r] {
				continue
			}
			if !yield(r) {
				return
			}
		}
	}
}

// before yields k's entries strictly before c in descending (Seq, ID)
// order; a zero c starts at the newest entry.
func (x *orderedIndex[K]) before(k K, c seqRef) iter.Seq[seqRef] {
	return func(yield func(seqRef) bool) {
		b, o, gone := x.base[k], x.over[k], x.gone[k]
		end := func(l []seqRef) int {
			if c == (seqRef{}) {
				return len(l)
			}
			return sort.Search(len(l), func(i int) bool { return !l[i].less(c) })
		}
		i, j := end(b)-1, end(o)-1
		for i >= 0 || j >= 0 {
			var r seqRef
			if j < 0 || i >= 0 && o[j].less(b[i]) {
				r, i = b[i], i-1
			} else {
				r, j = o[j], j-1
			}
			if gone[r] {
				continue
			}
			if !yield(r) {
				return
			}
		}
	}
}

// commit applies removals by binary search and additions by appending
// (the usual case: new entries carry the newest sequence numbers) or by
// binary-search insertion.
func (x *orderedIndex[K]) commit() {
	for k, gone := range x.gone {
		l := x.base[k]
		for r := range gone {
			x.commitWork++
			if i := search(l, r); i < len(l) && l[i] == r {
				l = slices.Delete(l, i, i+1)
			}
		}
		x.base[k] = l
	}
	for k, adds := range x.over {
		l := x.base[k]
		for _, r := range adds {
			if x.gone[k][r] {
				continue
			}
			x.commitWork++
			if len(l) == 0 || l[len(l)-1].less(r) {
				l = append(l, r)
			} else {
				l = slices.Insert(l, search(l, r), r)
			}
		}
		x.base[k] = l
	}
}

// mergeAfter yields the entries of several keys strictly after c, in one
// (Seq, ID) order, reading each key lazily.
func mergeAfter[K comparable](x *orderedIndex[K], keys []K, c seqRef) iter.Seq[seqRef] {
	return func(yield func(seqRef) bool) {
		type head struct {
			next func() (seqRef, bool)
			stop func()
			cur  seqRef
			ok   bool
		}
		heads := make([]*head, 0, len(keys))
		for _, k := range keys {
			next, stop := iter.Pull(x.after(k, c))
			h := &head{next: next, stop: stop}
			h.cur, h.ok = next()
			heads = append(heads, h)
		}
		defer func() {
			for _, h := range heads {
				h.stop()
			}
		}()
		for {
			var min *head
			for _, h := range heads {
				if h.ok && (min == nil || h.cur.less(min.cur)) {
					min = h
				}
			}
			if min == nil {
				return
			}
			r := min.cur
			min.cur, min.ok = min.next()
			if !yield(r) {
				return
			}
		}
	}
}
