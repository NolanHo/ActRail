package ws

import (
	"fmt"
	"sync"
)

type Registry struct {
	mu    sync.RWMutex
	items map[string]*ConnectionState
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]*ConnectionState)}
}

func (r *Registry) Add(conn *ConnectionState) error {
	if conn == nil {
		return fmt.Errorf("connection is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[conn.ID()]; ok {
		return fmt.Errorf("connection %q already registered", conn.ID())
	}
	r.items[conn.ID()] = conn
	return nil
}

func (r *Registry) Remove(connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, connectionID)
}

func (r *Registry) Get(connectionID string) (*ConnectionState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	conn, ok := r.items[connectionID]
	return conn, ok
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}
