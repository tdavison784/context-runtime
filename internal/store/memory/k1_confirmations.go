package memory

import (
	"sort"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// K1-api.3 confirmation records on the memory store (XREV-5.2): a broad
// raise — the ALL key or an ancestor-directory key — does not invalidate a
// CURRENT_PATH dependency on a path the same report explicitly confirmed
// with the path's prior content. The records are per (resource, confirmed
// path, broad key): the latest confirming raise, the latest unconfirmed
// raise it overtook, and the immutable closed runs of unconfirmed raises
// the settlement cause seeks (K1-api.3 SPEC-2).

// confKey keys one path's confirmation records for one broad affecting
// key; key "" is the ALL key, else a canonical resource-relative path.
type confKey struct {
	resource, path, key string
}

// gapRow is one closed run of unconfirmed raises of the key, from its
// first raise after the previous confirmation to the pointer the closing
// confirmation overtook. Rows are immutable once appended.
type gapRow struct {
	first, last uint64
}

// gapIndex holds the gap rows per confKey in increasing last order, seen
// through a transaction like a table: rows are only appended — each
// confirmation closes one run, ending at the key's raise pointer, which
// only rises.
type gapIndex struct {
	base map[confKey][]gapRow
	over map[confKey][]gapRow // nil in a read-only transaction
}

func newGapIndex(base map[confKey][]gapRow, writable bool) gapIndex {
	x := gapIndex{base: base}
	if writable {
		x.over = map[confKey][]gapRow{}
	}
	return x
}

func (x gapIndex) add(k confKey, r gapRow) { x.over[k] = append(x.over[k], r) }

// after returns the first row of k whose last revision exceeds last.
func (x gapIndex) after(k confKey, last uint64) (gapRow, bool) {
	seek := func(rows []gapRow) (gapRow, bool) {
		i := sort.Search(len(rows), func(i int) bool { return rows[i].last > last })
		if i == len(rows) {
			return gapRow{}, false
		}
		return rows[i], true
	}
	b, bok := seek(x.base[k])
	o, ook := seek(x.over[k])
	switch {
	case !bok:
		return o, ook
	case !ook || b.last <= o.last:
		return b, true
	default:
		return o, true
	}
}

func (x gapIndex) dirty() bool { return len(x.over) > 0 }

func (x gapIndex) commit() {
	for k, rows := range x.over {
		x.base[k] = append(x.base[k], rows...)
	}
}

// confirmKey validates and maps a confirmation read's arguments — path and
// key ("" for ALL, else canonical) — to its confKey.
func confirmKey(resourceID, path, key string) (confKey, error) {
	if _, err := store.PathAffectKeys(path); err != nil {
		return confKey{}, err
	}
	if key != "" {
		if _, err := store.PathAffectKeys(key); err != nil {
			return confKey{}, err
		}
	}
	return confKey{resourceID, path, key}, nil
}

// broadKeyCovers reports whether broad key K — "" for ALL, else a
// canonical path — covers path p: confirmation records are written only
// for such keys (K1-api.3 SPEC-1), so a write costs at most
// |confirmed| × depth.
func broadKeyCovers(K, p string) bool {
	if K == "" {
		return true
	}
	return len(p) > len(K) && p[len(K)] == '/' && p[:len(K)] == K
}

// lastRaiseRev is the raise index's current pointer for one affecting key.
func (t *tx) lastRaiseRev(resourceID, key string) uint64 {
	k, err := affectKey(resourceID, key)
	if err != nil {
		return 0 // unreachable: callers pass validated keys
	}
	for ref := range t.sem.res.affectRaises.before(k, seqRef{}) {
		return ref.seq
	}
	return 0
}

// confirmPath applies K1-api.3's write rule for one confirmed path under
// one raised broad key: if the previous raise did not confirm the path, it
// becomes the latest unconfirmed raise and its run is closed as a gap row;
// then the confirmation pointer moves to this report's revision. Runs
// before this report's own raise, so the key's pointer is the L of the
// rule.
func (t *tx) confirmPath(resource, updateID string, revision uint64, p, K string) {
	ck := confKey{resource, p, K}
	L := t.lastRaiseRev(resource, K)
	old, _ := t.sem.res.confirms.peek(ck)
	if old != L && L > 0 {
		t.sem.res.unconfirms.put(ck, L)
		k, _ := affectKey(resource, K)
		first := uint64(0)
		for ref := range t.sem.res.affectRaises.after(k, seqRef{old, raiseAfterID}) {
			first = ref.seq
			break
		}
		t.sem.res.gaps.add(ck, gapRow{first: first, last: L})
	}
	t.sem.res.confirms.put(ck, revision)
}

// LastConfirmedRev implements store.ResourceReader (K1-api.3): the latest
// broad raise of key that explicitly confirmed path's content unchanged,
// 0 when never.
func (r semRead) LastConfirmedRev(resourceID, path, key string) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	r.r.advanceK1()
	ck, err := confirmKey(resourceID, path, key)
	if err != nil {
		return 0, err
	}
	v, _ := r.r.sem.res.confirms.get(ck)
	return v, nil
}

// LastUnconfirmedRev implements store.ResourceReader (K1-api.3): the
// latest unconfirmed raise of key at or before the confirmation pointer,
// 0 when none.
func (r semRead) LastUnconfirmedRev(resourceID, path, key string) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	r.r.advanceK1()
	ck, err := confirmKey(resourceID, path, key)
	if err != nil {
		return 0, err
	}
	v, _ := r.r.sem.res.unconfirms.get(ck)
	return v, nil
}

// FirstUnconfirmedAffectingUpdateAfter implements store.ResourceReader
// (K1-api.3 SPEC-2): the earliest raise of broad key K past rev that did
// NOT confirm path — the settlement cause's gap-seek. The first closed
// unconfirmed run whose last revision passes rev names the seek floor;
// failing that, the open run after the confirmation pointer does;
// ErrNotFound when every raise of K past rev confirmed the path (or none
// is past rev).
func (r semRead) FirstUnconfirmedAffectingUpdateAfter(resourceID, path, key string, rev uint64) (domain.ResourceUpdate, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceUpdate{}, err
	}
	r.r.advanceK1()
	ck, err := confirmKey(resourceID, path, key)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	k, err := affectKey(resourceID, key)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	from := rev
	if gap, ok := r.r.sem.res.gaps.after(ck, rev); ok {
		if gap.first-1 > from {
			from = gap.first - 1
		}
	} else {
		var last uint64
		for ref := range r.r.sem.res.affectRaises.before(k, seqRef{}) {
			last = ref.seq
			break
		}
		conf, _ := r.r.sem.res.confirms.get(ck)
		if last == conf {
			return domain.ResourceUpdate{}, notFound("unconfirmed affecting update after", k.path)
		}
		if conf > from {
			from = conf
		}
	}
	for ref := range r.r.sem.res.affectRaises.after(k, seqRef{from, raiseAfterID}) {
		return r.raisedUpdate(ref, "unconfirmed affecting raise", k.path)
	}
	return domain.ResourceUpdate{}, notFound("unconfirmed affecting update after", k.path)
}
