package tiles

import (
	"container/list"
	"sync"
)

// lru is a bounded least-recently-used cache, safe for concurrent use.
type lru[K comparable, V any] struct {
	mu    sync.Mutex
	limit int
	order *list.List
	items map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRU[K comparable, V any](limit int) *lru[K, V] {
	return &lru[K, V]{limit: limit, order: list.New(), items: map[K]*list.Element{}}
}

func (c *lru[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		return element.Value.(*lruEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

func (c *lru[K, V]) put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		element.Value.(*lruEntry[K, V]).value = value
		c.order.MoveToFront(element)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry[K, V]{key: key, value: value})
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry[K, V]).key)
	}
}

func (c *lru[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
