package memory

import "iter"

// table is one record collection seen through a transaction: committed
// records in base, the transaction's uncommitted writes in over. Records are
// cloned on the way in and on the way out of get, so neither callers nor
// later writes can alias stored state. peek and all return stored values
// without cloning; callers must not mutate or retain them.
type table[K comparable, V any] struct {
	base  map[K]V
	over  map[K]V // nil in a read-only transaction
	clone func(V) V
}

func newTable[K comparable, V any](base map[K]V, writable bool, clone func(V) V) table[K, V] {
	t := table[K, V]{base: base, clone: clone}
	if writable {
		t.over = map[K]V{}
	}
	return t
}

func (t *table[K, V]) peek(k K) (V, bool) {
	if v, ok := t.over[k]; ok {
		return v, true
	}
	v, ok := t.base[k]
	return v, ok
}

func (t *table[K, V]) has(k K) bool {
	_, ok := t.peek(k)
	return ok
}

func (t *table[K, V]) get(k K) (V, bool) {
	v, ok := t.peek(k)
	if ok {
		v = t.clone(v)
	}
	return v, ok
}

func (t *table[K, V]) put(k K, v V) { t.over[k] = t.clone(v) }

// all yields every record, with overlay writes replacing committed ones.
func (t *table[K, V]) all() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for k, v := range t.base {
			if _, ok := t.over[k]; ok {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
		for k, v := range t.over {
			if !yield(k, v) {
				return
			}
		}
	}
}

// dirty reports whether the transaction wrote to the table.
func (t *table[K, V]) dirty() bool { return len(t.over) > 0 }

// commit folds the overlay into the committed records.
func (t *table[K, V]) commit() {
	for k, v := range t.over {
		t.base[k] = v
	}
}

func same[V any](v V) V { return v }
