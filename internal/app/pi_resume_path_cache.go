package app

import (
	"sync"
	"time"
)

const piResumePathCacheTTL = 10 * time.Second

type piResumePathCache struct {
	mu      sync.Mutex
	entries map[string]piResumePathCacheEntry
}

type piResumePathCacheEntry struct {
	paths []string
	ts    time.Time
}

func (c *piResumePathCache) paths(cwd string) []string {
	if c == nil {
		return listPIResumeSourcePaths(cwd)
	}
	now := time.Now().UTC()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]piResumePathCacheEntry{}
	}
	entry := c.entries[cwd]
	if len(entry.paths) > 0 && now.Sub(entry.ts) < piResumePathCacheTTL {
		paths := append([]string(nil), entry.paths...)
		c.mu.Unlock()
		return paths
	}
	c.mu.Unlock()

	paths := listPIResumeSourcePaths(cwd)

	c.mu.Lock()
	c.entries[cwd] = piResumePathCacheEntry{paths: append([]string(nil), paths...), ts: now}
	c.mu.Unlock()
	return paths
}
