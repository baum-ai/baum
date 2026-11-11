package redact

import (
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync"
)

// Memo remembers what texts masked to, so a text masked again unchanged,
// such as a running session's title every second, is not scanned again. It
// holds masked text only: an entry is found by a salted SHA-256 of its input,
// never by the input itself. It keeps at most maxEntries entries and
// maxBytes bytes of masked text, dropping the least recently used, and does
// not keep a text over a quarter of maxBytes. It forgets everything when it
// is asked with an engine of other rules, so a change of rules takes effect
// on the next call. It is safe for concurrent use; a nil Memo remembers
// nothing.
type Memo struct {
	maxEntries, maxBytes int
	salt                 [32]byte

	mu      sync.Mutex
	rules   string
	entries map[[sha256.Size]byte]*list.Element
	// order holds *memoEntry, the most recently used first.
	order list.List
	bytes int
}

type memoEntry struct {
	key    [sha256.Size]byte
	masked string
}

// NewMemo returns an empty memo of at most maxEntries entries and maxBytes
// bytes of masked text.
func NewMemo(maxEntries, maxBytes int) *Memo {
	m := &Memo{maxEntries: maxEntries, maxBytes: maxBytes, entries: map[[sha256.Size]byte]*list.Element{}}
	_, _ = rand.Read(m.salt[:])
	return m
}

// Redact returns text as engine.RedactContext masks it. On an error it
// returns "" with the error and remembers nothing.
func (m *Memo) Redact(ctx context.Context, engine *Engine, text string) (string, error) {
	if m == nil || text == "" {
		masked, _, err := engine.RedactContext(ctx, text)
		if err != nil {
			return "", err
		}
		return masked, nil
	}
	key := m.key(text)
	if masked, ok := m.lookup(engine.rules, key); ok {
		return masked, nil
	}
	masked, _, err := engine.RedactContext(ctx, text)
	if err != nil {
		return "", err
	}
	m.store(engine.rules, key, masked)
	return masked, nil
}

// Len is the number of texts remembered.
func (m *Memo) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order.Len()
}

func (m *Memo) key(text string) [sha256.Size]byte {
	h := sha256.New()
	h.Write(m.salt[:])
	h.Write([]byte(text))
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

func (m *Memo) lookup(rules string, key [sha256.Size]byte) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetOtherRules(rules)
	el, ok := m.entries[key]
	if !ok {
		return "", false
	}
	m.order.MoveToFront(el)
	return el.Value.(*memoEntry).masked, true
}

func (m *Memo) store(rules string, key [sha256.Size]byte, masked string) {
	if len(masked) > m.maxBytes/4 || m.maxEntries <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetOtherRules(rules)
	if el, ok := m.entries[key]; ok {
		m.order.MoveToFront(el)
		return
	}
	m.entries[key] = m.order.PushFront(&memoEntry{key: key, masked: masked})
	m.bytes += len(masked)
	for m.order.Len() > m.maxEntries || m.bytes > m.maxBytes {
		oldest := m.order.Back()
		entry := m.order.Remove(oldest).(*memoEntry)
		delete(m.entries, entry.key)
		m.bytes -= len(entry.masked)
	}
}

// forgetOtherRules drops every entry when rules are not the ones they were
// masked with. m.mu is held.
func (m *Memo) forgetOtherRules(rules string) {
	if rules == m.rules {
		return
	}
	m.rules = rules
	clear(m.entries)
	m.order.Init()
	m.bytes = 0
}
