package store

import "time"

// CacheBackend is the narrow slice of a Postgres or in-memory store that
// LLMCache delegates to. Both *DB (Postgres) and *MemoryStore (in-memory
// demo mode) satisfy it, so the synthesizer can stay backend-agnostic.
type CacheBackend interface {
	CacheGet(key string, maxAge time.Duration) string
	CachePut(key, response, model string)
}

// LLMCache wraps a backend's cache operations for use as an analysis.ClaimCache.
type LLMCache struct {
	backend CacheBackend
	maxAge  time.Duration
}

// NewLLMCache creates a cache adapter with the given TTL. backend may be
// any CacheBackend implementation; nil disables caching (Get returns "",
// Put no-ops via the nilCache fallback).
func NewLLMCache(backend CacheBackend, maxAge time.Duration) *LLMCache {
	if backend == nil {
		backend = nilCache{}
	}
	return &LLMCache{backend: backend, maxAge: maxAge}
}

func (c *LLMCache) Get(key string) string {
	return c.backend.CacheGet(key, c.maxAge)
}

func (c *LLMCache) Put(key, value, model string) {
	c.backend.CachePut(key, value, model)
}

// nilCache is a no-op CacheBackend for callers that don't want caching.
type nilCache struct{}

func (nilCache) CacheGet(string, time.Duration) string { return "" }
func (nilCache) CachePut(string, string, string)       {}
