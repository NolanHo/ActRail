package app

import (
	"container/list"
	"sync"

	"actrail/internal/domain/session"
)

const defaultSessionMessageCacheEntries = 64

type sessionMessageCache struct {
	mu       sync.Mutex
	limit    int
	items    map[session.SessionID]*list.Element
	lru      *list.List
}

type sessionMessageCacheEntry struct {
	sessionID session.SessionID
	signature string
	items     []SessionMessage
}

func newSessionMessageCache(limit int) *sessionMessageCache {
	if limit <= 0 {
		limit = defaultSessionMessageCacheEntries
	}
	return &sessionMessageCache{
		limit: limit,
		items: make(map[session.SessionID]*list.Element),
		lru:   list.New(),
	}
}

func (c *sessionMessageCache) GetSession(sessionID session.SessionID) ([]SessionMessage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return nil, false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	c.lru.MoveToFront(elem)
	return cloneSessionMessages(entry.items), true
}

func (c *sessionMessageCache) Get(sessionID session.SessionID, signature string) ([]SessionMessage, bool) {
	if c == nil || signature == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return nil, false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	if entry.signature != signature {
		c.lru.Remove(elem)
		delete(c.items, sessionID)
		return nil, false
	}
	c.lru.MoveToFront(elem)
	return cloneSessionMessages(entry.items), true
}

func (c *sessionMessageCache) GetPage(sessionID session.SessionID, signature string, req SessionMessagesRequest) (SessionMessagesResponse, bool) {
	if c == nil || signature == "" {
		return SessionMessagesResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return SessionMessagesResponse{}, false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	if entry.signature != signature {
		c.lru.Remove(elem)
		delete(c.items, sessionID)
		return SessionMessagesResponse{}, false
	}
	c.lru.MoveToFront(elem)
	return paginateSessionMessagesForRequest(entry.items, req), true
}

func (c *sessionMessageCache) Put(sessionID session.SessionID, signature string, items []SessionMessage) {
	if c == nil || signature == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem := c.items[sessionID]; elem != nil {
		entry := elem.Value.(*sessionMessageCacheEntry)
		entry.signature = signature
		entry.items = cloneSessionMessages(items)
		c.lru.MoveToFront(elem)
		return
	}
	elem := c.lru.PushFront(&sessionMessageCacheEntry{
		sessionID: sessionID,
		signature: signature,
		items:     cloneSessionMessages(items),
	})
	c.items[sessionID] = elem
	for c.lru.Len() > c.limit {
		back := c.lru.Back()
		if back == nil {
			return
		}
		entry := back.Value.(*sessionMessageCacheEntry)
		delete(c.items, entry.sessionID)
		c.lru.Remove(back)
	}
}

func (c *sessionMessageCache) Invalidate(sessionID session.SessionID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem := c.items[sessionID]; elem != nil {
		c.lru.Remove(elem)
		delete(c.items, sessionID)
	}
}

func cloneSessionMessages(items []SessionMessage) []SessionMessage {
	out := make([]SessionMessage, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Details != nil {
			details := make(map[string]any, len(out[i].Details))
			for k, v := range out[i].Details {
				details[k] = v
			}
			out[i].Details = details
		}
	}
	return out
}
