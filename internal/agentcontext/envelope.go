package agentcontext

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/untrusted"
)

const (
	// MaxChunk bounds the data of one tool result.
	MaxChunk = 64 << 10
	// A consumer's unread continuations: at most maxPending, each for
	// pendingTTL. The oldest goes first.
	maxPending = 8
	pendingTTL = 15 * time.Minute
)

var errCursor = errors.New("unknown or expired cursor: call the tool again")

// envelope turns tool output into what an agent receives: masked, cut into
// chunks of at most MaxChunk bytes, and fenced as untrusted data. What is
// left after a chunk is kept, masked, in memory under a random cursor the
// agent passes to read_more; each cursor gives its chunk once.
type envelope struct {
	now     func() time.Time
	mu      sync.Mutex
	pending map[string]*continuation
}

type continuation struct {
	consumer string
	rest     string
	expires  time.Time
	created  time.Time
}

func newEnvelope() *envelope {
	return &envelope{now: time.Now, pending: map[string]*continuation{}}
}

// wrap masks text and returns its first chunk.
func (e *envelope) wrap(ctx context.Context, consumer, text string) (string, error) {
	// The redactor's rules are compiled once per process.
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return "", err
	}
	masked, _, err := redactor.RedactString(ctx, text)
	if err != nil {
		return "", err
	}
	return e.chunk(consumer, masked), nil
}

// more returns the chunk a cursor holds, once.
func (e *envelope) more(consumer, cursor string) (string, error) {
	e.mu.Lock()
	c, ok := e.pending[cursor]
	if ok && c.consumer == consumer {
		delete(e.pending, cursor)
	}
	e.mu.Unlock()
	if !ok || c.consumer != consumer || !e.now().Before(c.expires) {
		return "", errCursor
	}
	return e.chunk(consumer, c.rest), nil
}

// chunk fences masked text's first chunk and keeps the rest.
func (e *envelope) chunk(consumer, masked string) string {
	head, rest := cut(masked, MaxChunk)
	out := untrusted.New().Block(head)
	if rest == "" {
		return out
	}
	cursor := random()
	now := e.now()
	e.mu.Lock()
	e.expire(now)
	e.pending[cursor] = &continuation{consumer: consumer, rest: rest, expires: now.Add(pendingTTL), created: now}
	e.trim(consumer)
	e.mu.Unlock()
	return out + fmt.Sprintf("\nThe result was cut after %d bytes; %d bytes remain. Call read_more with cursor %q for the next part.\n", len(head), len(rest), cursor)
}

func (e *envelope) expire(now time.Time) {
	for cursor, c := range e.pending {
		if !now.Before(c.expires) {
			delete(e.pending, cursor)
		}
	}
}

// trim drops the consumer's oldest continuations beyond maxPending.
func (e *envelope) trim(consumer string) {
	for {
		count, oldest := 0, ""
		for cursor, c := range e.pending {
			if c.consumer != consumer {
				continue
			}
			count++
			if oldest == "" || c.created.Before(e.pending[oldest].created) {
				oldest = cursor
			}
		}
		if count <= maxPending {
			return
		}
		delete(e.pending, oldest)
	}
}

func (e *envelope) forget(consumer string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for cursor, c := range e.pending {
		if c.consumer == consumer {
			delete(e.pending, cursor)
		}
	}
}

// cut splits s after at most n bytes, at a UTF-8 boundary. Text that is not
// UTF-8 there is cut at n.
func cut(s string, n int) (string, string) {
	if len(s) <= n {
		return s, ""
	}
	i := n
	for i > n-utf8.UTFMax+1 && !utf8.RuneStart(s[i]) {
		i--
	}
	if !utf8.RuneStart(s[i]) {
		i = n
	}
	return s[:i], s[i:]
}
