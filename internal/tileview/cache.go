package tileview

import (
	"sync"
)

const (
	// maxAnswers and maxBytes bound a Cache by count and by size (each
	// answer's body and the HTML of its frames); the least recently used
	// answer goes first. The newest answer is always kept, whatever its size.
	maxAnswers = 16
	maxBytes   = 64 << 20
)

type answer struct {
	key    string
	answer Answer
}

func (a answer) size() int { return len(a.answer.Body.Data) + a.answer.Frames.Size() }

// Cache keeps encoded tile answers under keys that name the bytes they were
// built from, so a document asked for again is neither read nor converted
// again. An answer it returns has its frames registered again, so a frame
// document the registry dropped comes back with the answer that names it.
// The zero value is an empty cache; it is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	answers []answer // least recently used first
	bytes   int
}

// Get returns the answer kept under key; an empty key is never kept.
func (c *Cache) Get(key string) (Answer, bool) {
	if key == "" {
		return Answer{}, false
	}
	c.mu.Lock()
	var found Answer
	ok := false
	for i, a := range c.answers {
		if a.key == key {
			copy(c.answers[i:], c.answers[i+1:])
			c.answers[len(c.answers)-1] = a
			found, ok = a.answer, true
			break
		}
	}
	c.mu.Unlock()
	if ok {
		found.Frames.Register()
	}
	return found, ok
}

// Put keeps an answer under key, dropping the least recently used answers
// beyond the bounds.
func (c *Cache) Put(key string, a Answer) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, old := range c.answers {
		if old.key == key {
			c.bytes -= old.size()
			c.removeAt(i)
			break
		}
	}
	kept := answer{key, a}
	c.answers = append(c.answers, kept)
	c.bytes += kept.size()
	for len(c.answers) > 1 && (len(c.answers) > maxAnswers || c.bytes > maxBytes) {
		c.bytes -= c.answers[0].size()
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
