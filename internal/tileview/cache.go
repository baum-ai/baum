package tileview

import (
	"sync"

	"github.com/baum-ai/baum/internal/etagjson"
)

const (
	// maxAnswers and maxBytes bound a Cache by count and by size; the least
	// recently used answer goes first. The newest answer is always kept,
	// whatever its size.
	maxAnswers = 16
	maxBytes   = 64 << 20
)

type answer struct {
	key  string
	body etagjson.Body
}

// Cache keeps encoded tile answers under keys that name the bytes they were
// built from, so a document asked for again is neither read nor converted
// again. The zero value is an empty cache; it is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	answers []answer // least recently used first
	bytes   int
}

// Get returns the answer kept under key; an empty key is never kept.
func (c *Cache) Get(key string) (etagjson.Body, bool) {
	if key == "" {
		return etagjson.Body{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, a := range c.answers {
		if a.key == key {
			copy(c.answers[i:], c.answers[i+1:])
			c.answers[len(c.answers)-1] = a
			return a.body, true
		}
	}
	return etagjson.Body{}, false
}

// Put keeps body under key, dropping the least recently used answers beyond
// the bounds.
func (c *Cache) Put(key string, body etagjson.Body) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, old := range c.answers {
		if old.key == key {
			c.bytes -= len(old.body.Data)
			c.removeAt(i)
			break
		}
	}
	c.answers = append(c.answers, answer{key, body})
	c.bytes += len(body.Data)
	for len(c.answers) > 1 && (len(c.answers) > maxAnswers || c.bytes > maxBytes) {
		c.bytes -= len(c.answers[0].body.Data)
		c.removeAt(0)
	}
}

// removeAt removes the answer at i and clears the slot the slice no longer
// covers, so a dropped body is not kept reachable by the backing array.
func (c *Cache) removeAt(i int) {
	copy(c.answers[i:], c.answers[i+1:])
	c.answers[len(c.answers)-1] = answer{}
	c.answers = c.answers[:len(c.answers)-1]
}
