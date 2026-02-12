package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"qmdsr/config"
	"qmdsr/model"
)

type Entry struct {
	Results           []model.SearchResult
	IndexVersion      string
	CreatedAt         time.Time
	Query             string
	Mode              string
	Collection        string
	Collections       []string
	FallbackTriggered bool
	Degraded          bool
	DegradeReason     string
}

type Cache struct {
	mu           sync.RWMutex
	items        map[string]*list.Element
	order        *list.List
	maxEntries   int
	ttl          time.Duration
	versionAware bool
	version      string
	enabled      bool

	hits   int64
	misses int64
}

type cacheItem struct {
	key   string
	entry Entry
}

func New(cfg *config.CacheConfig) *Cache {
	return &Cache{
		items:        make(map[string]*list.Element),
		order:        list.New(),
		maxEntries:   cfg.MaxEntries,
		ttl:          cfg.TTL,
		versionAware: cfg.VersionAware,
		enabled:      cfg.Enabled,
	}
}

func (c *Cache) Get(key string) (*Entry, bool) {
	if !c.enabled {
		return nil, false
	}

	c.mu.RLock()
	elem, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	item := elem.Value.(*cacheItem)

	if time.Since(item.entry.CreatedAt) > c.ttl {
		c.mu.Lock()
		c.remove(key)
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	if c.versionAware && item.entry.IndexVersion != c.version {
		c.mu.Lock()
		c.remove(key)
		c.misses++
		c.mu.Unlock()
		return nil, false
	}

	c.mu.Lock()
	c.order.MoveToFront(elem)
	c.hits++
	c.mu.Unlock()

	return &item.entry, true
}

func (c *Cache) Put(key string, entry Entry) {
	if !c.enabled {
		return
	}

	entry.IndexVersion = c.version
	entry.CreatedAt = time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*cacheItem).entry = entry
		return
	}

	if c.order.Len() >= c.maxEntries {
		c.evict()
	}

	item := &cacheItem{key: key, entry: entry}
	elem := c.order.PushFront(item)
	c.items[key] = elem
}

func (c *Cache) SetVersion(version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version = version
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
}

func (c *Cache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for key, elem := range c.items {
		item := elem.Value.(*cacheItem)
		expired := time.Since(item.entry.CreatedAt) > c.ttl
		versionMismatch := c.versionAware && item.entry.IndexVersion != c.version
		if expired || versionMismatch {
			c.order.Remove(elem)
			delete(c.items, key)
			removed++
		}
	}
	return removed
}

func (c *Cache) Stats() (size int, hits, misses int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len(), c.hits, c.misses
}

func (c *Cache) Healthy() bool {
	defer func() { recover() }()
	c.mu.RLock()
	_ = c.order.Len()
	c.mu.RUnlock()
	return true
}

func (c *Cache) remove(key string) {
	if elem, ok := c.items[key]; ok {
		c.order.Remove(elem)
		delete(c.items, key)
	}
}

func (c *Cache) evict() {
	back := c.order.Back()
	if back == nil {
		return
	}
	item := back.Value.(*cacheItem)
	c.order.Remove(back)
	delete(c.items, item.key)
}

func MakeCacheKey(query, mode, collection string, minScore float64, n int, fallback bool) string {
	raw := struct {
		Query      string  `json:"q"`
		Mode       string  `json:"m"`
		Collection string  `json:"c"`
		MinScore   float64 `json:"s"`
		N          int     `json:"n"`
		Fallback   bool    `json:"f"`
	}{query, mode, collection, minScore, n, fallback}
	data, _ := json.Marshal(raw)
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
