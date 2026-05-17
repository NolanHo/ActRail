package app

import (
	"container/list"
	"sync"

	"actrail/internal/domain/session"
)

const defaultSessionMessageCacheEntries = 64

type sessionMessageCache struct {
	mu    sync.Mutex
	limit int
	items map[session.SessionID]*list.Element
	lru   *list.List
}

type sessionMessageCacheEntry struct {
	sessionID session.SessionID
	signature string
	items     []SessionMessage
	complete  bool
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
	items, _, ok := c.GetWithCompletion(sessionID, signature)
	return items, ok
}

func (c *sessionMessageCache) GetWithCompletion(sessionID session.SessionID, signature string) ([]SessionMessage, bool, bool) {
	if c == nil || signature == "" {
		return nil, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return nil, false, false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	if entry.signature != signature {
		c.lru.Remove(elem)
		delete(c.items, sessionID)
		return nil, false, false
	}
	c.lru.MoveToFront(elem)
	return cloneSessionMessages(entry.items), entry.complete, true
}

func (c *sessionMessageCache) Has(sessionID session.SessionID, signature string) bool {
	if c == nil || signature == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	if entry.signature != signature {
		return false
	}
	return true
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

func (c *sessionMessageCache) GetPageWithCompletion(sessionID session.SessionID, signature string, req SessionMessagesRequest) (SessionMessagesResponse, bool, bool) {
	if c == nil || signature == "" {
		return SessionMessagesResponse{}, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.items[sessionID]
	if elem == nil {
		return SessionMessagesResponse{}, false, false
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	if entry.signature != signature {
		c.lru.Remove(elem)
		delete(c.items, sessionID)
		return SessionMessagesResponse{}, false, false
	}
	c.lru.MoveToFront(elem)
	return paginateSessionMessagesForRequest(entry.items, req), entry.complete, true
}

func (c *sessionMessageCache) Put(sessionID session.SessionID, signature string, items []SessionMessage) {
	c.PutWithCompletion(sessionID, signature, items, false)
}

func (c *sessionMessageCache) PutWithCompletion(sessionID session.SessionID, signature string, items []SessionMessage, complete bool) {
	if c == nil || signature == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem := c.items[sessionID]; elem != nil {
		entry := elem.Value.(*sessionMessageCacheEntry)
		entry.signature = signature
		entry.items = cloneSessionMessages(items)
		entry.complete = complete
		c.lru.MoveToFront(elem)
		return
	}
	elem := c.lru.PushFront(&sessionMessageCacheEntry{
		sessionID: sessionID,
		signature: signature,
		items:     cloneSessionMessages(items),
		complete:  complete,
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
