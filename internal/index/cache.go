package index

import (
	"container/list"
	"sync"
)

// CachePage is a bounded decoded index/payload page.
type CachePage struct {
	Key   string
	Bytes int
	Value any
}

// PageCache evicts least-recently-used pages while retaining at most maxBytes.
// The release configuration uses MaxPageCacheBytes (4 MiB).
type PageCache struct {
	mu       sync.Mutex
	maxBytes int
	current  int
	ll       *list.List
	items    map[string]*list.Element
}

func NewPageCache(maxBytes int) *PageCache {
	if maxBytes <= 0 {
		maxBytes = MaxPageCacheBytes
	}
	return &PageCache{maxBytes: maxBytes, ll: list.New(), items: map[string]*list.Element{}}
}

type cacheEntry struct {
	page CachePage
}

// Get returns a cached page.
func (c *PageCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).page.Value, true
}

// Put inserts a page and evicts older pages to honor the byte ceiling.
// A single page larger than the ceiling is never retained.
func (c *PageCache) Put(page CachePage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[page.Key]; ok {
		old := el.Value.(*cacheEntry).page.Bytes
		c.current -= old
		c.ll.Remove(el)
		delete(c.items, page.Key)
	}
	if page.Bytes > c.maxBytes {
		return
	}
	el := c.ll.PushFront(&cacheEntry{page: page})
	c.items[page.Key] = el
	c.current += page.Bytes
	for c.current > c.maxBytes && c.ll.Len() > 0 {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*cacheEntry)
		c.ll.Remove(oldest)
		delete(c.items, entry.page.Key)
		c.current -= entry.page.Bytes
	}
}

// Bytes returns the currently retained byte estimate.
func (c *PageCache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Len returns cached page count.
func (c *PageCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
