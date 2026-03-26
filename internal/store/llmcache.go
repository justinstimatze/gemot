package store

import "time"

// LLMCache wraps the DB's cache operations for use as an analysis.ClaimCache.
type LLMCache struct {
	db     *DB
	maxAge time.Duration
}

// NewLLMCache creates a cache adapter with the given TTL.
func NewLLMCache(db *DB, maxAge time.Duration) *LLMCache {
	return &LLMCache{db: db, maxAge: maxAge}
}

func (c *LLMCache) Get(key string) string {
	return c.db.CacheGet(key, c.maxAge)
}

func (c *LLMCache) Put(key, value, model string) {
	c.db.CachePut(key, value, model)
}
