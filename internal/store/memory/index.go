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
