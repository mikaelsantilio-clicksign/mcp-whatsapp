package api

import (
	"sync"
	"time"
)

// idempotencyCache deduplicates incoming messages by message_id within a
// short TTL window, protecting against n8n retries.
type idempotencyCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newIdempotencyCache(ttl time.Duration) *idempotencyCache {
	return &idempotencyCache{ttl: ttl, seen: make(map[string]time.Time)}
}

// SeenRecently returns true if the id was processed within the TTL window.
// If the id is new (or expired), it is recorded and the function returns false.
func (c *idempotencyCache) SeenRecently(id string) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if expiry, ok := c.seen[id]; ok && now.Before(expiry) {
		return true
	}
	// Opportunistic GC: drop a few expired entries on each insert.
	for k, exp := range c.seen {
		if now.After(exp) {
			delete(c.seen, k)
			break
		}
	}
	c.seen[id] = now.Add(c.ttl)
	return false
}
