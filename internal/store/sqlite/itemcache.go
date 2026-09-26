package sqlite

import (
	"container/list"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Baseline caps on the per-transaction item cache (SPEC-4.1). The byte cap
// grows to twice the largest transcript read, so configured spans above the
// default size can still be cached. Other items never raise the cap.
const (
	itemCacheMaxBytes   = 16 << 20
	itemCacheMaxEntries = 1024
)

// itemCache is a least-recently-used cache of verified items, bounded by
// entry count and text bytes, so a transaction that touches many large
// items keeps only a bounded amount of them (SPEC-4.1). Ingest re-reads a
// span's transcript once per derived item (SPEC-3.1 item 2), so the cache
// accommodates two largest-seen transcripts. A nil cache is empty.
type itemCache struct {
	order    list.List // front is most recently used; values are *cacheEntry
	entries  map[string]*list.Element
	bytes    int
	maxBytes int
}

type cacheEntry struct {
	id   string
	item domain.ContextItem
	cost int
}

func itemCost(v domain.ContextItem) int {
	n := 0
	for _, p := range v.Parts {
		n += len(p.Text)
	}
	return n
}

// get returns the cached item (not a clone) and marks it recently used.
func (c *itemCache) get(id string) (domain.ContextItem, bool) {
	if c == nil {
		return domain.ContextItem{}, false
	}
	e, ok := c.entries[id]
	if !ok {
		return domain.ContextItem{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*cacheEntry).item, true
}

// put stores v (the caller passes a clone it no longer uses) and evicts the
// least recently used entries until both caps hold.
func (c *itemCache) put(id string, v domain.ContextItem) {
	c.remove(id)
	cost := itemCost(v)
	if v.Role == domain.RoleTranscript && cost > itemCacheMaxBytes/2 {
		if cost > int(^uint(0)>>1)/2 {
			return // a cap that cannot represent two transcripts cannot be safe
		}
		c.maxBytes = max(c.maxBytes, 2*cost)
	}
	limit := max(itemCacheMaxBytes, c.maxBytes)
	if cost > limit {
		return
	}
	for c.order.Len() >= itemCacheMaxEntries || c.bytes > limit-cost {
		c.remove(c.order.Back().Value.(*cacheEntry).id)
	}
	if c.entries == nil {
		c.entries = map[string]*list.Element{}
	}
	c.entries[id] = c.order.PushFront(&cacheEntry{id: id, item: v, cost: cost})
	c.bytes += cost
}

// remove drops id if cached.
func (c *itemCache) remove(id string) {
	if c == nil {
		return
	}
	if e, ok := c.entries[id]; ok {
		c.bytes -= e.Value.(*cacheEntry).cost
		c.order.Remove(e)
		delete(c.entries, id)
	}
}

// footprint reports the entry count and text bytes held.
func (c *itemCache) footprint() (entries, bytes int) {
	if c == nil {
		return 0, 0
	}
	return c.order.Len(), c.bytes
}
