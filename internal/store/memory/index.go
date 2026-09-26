package memory

import "iter"

// index is an append-only multimap from a key to record IDs, seen through a
// transaction like a table. Relationships are immutable and never deleted,
// so an index only grows: a transaction appends to its overlay and commit
// appends the overlay to the committed lists.
type index[K comparable] struct {
	base map[K][]string
	over map[K][]string // nil in a read-only transaction
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
