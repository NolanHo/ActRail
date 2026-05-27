package app

import (
	"container/list"
	"sync"

	"actrail/internal/domain/session"
)

const defaultSessionMessageCacheEntries = 64
const defaultSessionMessageCacheMaxBytes = 256 * 1024 * 1024
const defaultSessionMessageCacheMaxEntryBytes = 16 * 1024 * 1024

type sessionMessageCache struct {
	mu            sync.Mutex
	limit         int
	maxBytes      int
	maxEntryBytes int
	bytes         int
	items         map[session.SessionID]*list.Element
	lru           *list.List
}

type sessionMessageCacheEntry struct {
	sessionID session.SessionID
	signature string
	items     []SessionMessage
	complete  bool
	bytes     int
}

func newSessionMessageCache(limit int) *sessionMessageCache {
	return newSessionMessageCacheWithBudget(limit, defaultSessionMessageCacheMaxBytes, defaultSessionMessageCacheMaxEntryBytes)
}

func newSessionMessageCacheWithBudget(limit int, maxBytes int, maxEntryBytes int) *sessionMessageCache {
	if limit <= 0 {
		limit = defaultSessionMessageCacheEntries
	}
	if maxBytes <= 0 {
		maxBytes = defaultSessionMessageCacheMaxBytes
	}
	if maxEntryBytes <= 0 || maxEntryBytes > maxBytes {
		maxEntryBytes = maxBytes
	}
	return &sessionMessageCache{
		limit:         limit,
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		items:         make(map[session.SessionID]*list.Element),
		lru:           list.New(),
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
		c.removeLocked(elem)
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
		c.removeLocked(elem)
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
		c.removeLocked(elem)
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
	entryBytes := estimateSessionMessagesBytes(items)
	c.mu.Lock()
	defer c.mu.Unlock()
	if entryBytes > c.maxEntryBytes {
		if elem := c.items[sessionID]; elem != nil {
			c.removeLocked(elem)
		}
		return
	}
	if elem := c.items[sessionID]; elem != nil {
		entry := elem.Value.(*sessionMessageCacheEntry)
		c.bytes -= entry.bytes
		entry.signature = signature
		entry.items = cloneSessionMessages(items)
		entry.complete = complete
		entry.bytes = entryBytes
		c.bytes += entryBytes
		c.lru.MoveToFront(elem)
		c.enforceBudgetLocked()
		return
	}
	elem := c.lru.PushFront(&sessionMessageCacheEntry{
		sessionID: sessionID,
		signature: signature,
		items:     cloneSessionMessages(items),
		complete:  complete,
		bytes:     entryBytes,
	})
	c.items[sessionID] = elem
	c.bytes += entryBytes
	c.enforceBudgetLocked()
}

func (c *sessionMessageCache) Invalidate(sessionID session.SessionID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem := c.items[sessionID]; elem != nil {
		c.removeLocked(elem)
	}
}

func (c *sessionMessageCache) enforceBudgetLocked() {
	for c.lru.Len() > c.limit || c.bytes > c.maxBytes {
		back := c.lru.Back()
		if back == nil {
			return
		}
		c.removeLocked(back)
	}
}

func (c *sessionMessageCache) removeLocked(elem *list.Element) {
	if elem == nil {
		return
	}
	entry := elem.Value.(*sessionMessageCacheEntry)
	delete(c.items, entry.sessionID)
	c.bytes -= entry.bytes
	if c.bytes < 0 {
		c.bytes = 0
	}
	c.lru.Remove(elem)
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

func estimateSessionMessagesBytes(items []SessionMessage) int {
	total := 0
	for i := range items {
		total += 256
		total += len(items[i].Role) + len(items[i].Kind) + len(items[i].Type) + len(items[i].Text)
		total += len(items[i].SessionID) + len(items[i].EventID) + len(items[i].ParentEventID)
		total += len(items[i].SourceOrder) + len(items[i].Name) + len(items[i].Summary) + len(items[i].ToolCallID)
		total += estimateAnyBytes(items[i].Details, 0)
		total += len(items[i].SupervisorRuns) * 512
	}
	return total
}

func estimateAnyBytes(value any, depth int) int {
	if value == nil || depth > 8 {
		return 0
	}
	switch typed := value.(type) {
	case string:
		return len(typed)
	case []byte:
		return len(typed)
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return 16
	case map[string]any:
		total := 0
		for key, nested := range typed {
			total += len(key) + estimateAnyBytes(nested, depth+1)
		}
		return total
	case []any:
		total := 0
		for _, nested := range typed {
			total += estimateAnyBytes(nested, depth+1)
		}
		return total
	default:
		return 128
	}
}
