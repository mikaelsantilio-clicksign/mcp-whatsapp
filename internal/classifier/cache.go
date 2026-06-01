package classifier

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// cache deduplicates classification requests by (phone, content-fingerprint)
// in a short TTL window. Same message + same recent context yields the same
// verdict, so a small cache avoids paying for repeat OpenAI calls during
// rapid back-and-forth.
type cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]cacheEntry
}

type cacheEntry struct {
	verdict Verdict
	expiry  time.Time
}

func newCache(ttl time.Duration) *cache {
	return &cache{ttl: ttl, items: make(map[string]cacheEntry)}
}

// Get returns a cached verdict if present and unexpired.
func (c *cache) Get(key string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok && time.Now().Before(e.expiry) {
		return e.verdict, true
	}
	return Verdict{}, false
}

// Put stores a verdict under the given key with the configured TTL.
func (c *cache) Put(key string, v Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{verdict: v, expiry: time.Now().Add(c.ttl)}
	// Opportunistic GC.
	if len(c.items) > 256 {
		for k, e := range c.items {
			if time.Now().After(e.expiry) {
				delete(c.items, k)
			}
		}
	}
}

// fingerprintKey builds a stable cache key from the message and recent
// context. The same input/context combo across calls produces the same
// fingerprint.
func fingerprintKey(message string, recent []HistoryTurn) string {
	h := sha256.New()
	h.Write([]byte(message))
	h.Write([]byte{0})
	for _, t := range recent {
		h.Write([]byte(t.Role))
		h.Write([]byte{0})
		h.Write([]byte(t.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
