// Copyright 2026 cloudygreybeard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8s

import (
	"sync"
	"time"
)

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

// Cache is a thread-safe TTL-based cache for arbitrary values.
type Cache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]cacheEntry
}

// NewCache creates a cache with the given TTL for entries.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl:     ttl,
		entries: make(map[string]cacheEntry),
	}
}

// Get retrieves a cached value. Returns the value and true if found
// and not expired, otherwise nil and false.
func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			c.mu.Lock()
			delete(c.entries, key)
			c.mu.Unlock()
		}
		return nil, false
	}
	return entry.value, true
}

// Set stores a value in the cache with the default TTL.
func (c *Cache) Set(key string, value any) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// Delete removes a specific key from the cache.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// DeletePrefix removes all keys with the given prefix.
func (c *Cache) DeletePrefix(prefix string) {
	c.mu.Lock()
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}
