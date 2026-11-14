// Package tileframes keeps the frame documents of custom tile types: the
// documents the app server serves at /tile-frames/<hex>, each the kit's
// composition (kit.Compose) of one type's HTML, keyed by the type's
// definition digest.
//
// The registry holds only definitions the daemon itself handed to the
// authenticated app: the tile routes register a document's ready custom
// tiles as they serve it, and the render check registers a candidate when
// it hands it to the app's check runner (internal/tilechecks). Nothing a
// request carries is ever registered, so the frame route serves only bytes
// the app already received.
package tileframes

import (
	"container/list"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
)

// MaxBytes bounds the composed documents a registry keeps: at least 64,
// since a composed document is at most kit.MaxDocument (512 KiB).
const MaxBytes = 32 << 20

// digestPattern is a definition digest as the host writes it.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	// ErrDigest is a definition whose digest is malformed or does not match
	// its content.
	ErrDigest = errors.New("tileframes: the definition does not match its digest")
	// ErrFormat is a source format other than html.
	ErrFormat = errors.New("tileframes: the definition's source is not html")
)

// Composed counts the documents registries have composed, for tests.
var Composed atomic.Int64

// Registry is a bounded, least recently used set of frame documents. The
// zero value is not usable; New makes one. It is safe for concurrent use.
type Registry struct {
	max     int
	mu      sync.Mutex
	order   *list.List // of *frame, most recently used first
	byKey   map[string]*list.Element
	bytes   int
	hung    map[string]bool
	evicted int
}

type frame struct {
	digest   string
	document []byte
}

// New returns an empty registry that keeps at most maxBytes of documents;
// the newest is kept whatever its size.
func New(maxBytes int) *Registry {
	return &Registry{max: maxBytes, order: list.New(), byKey: map[string]*list.Element{}, hung: map[string]bool{}}
}

// Add registers the frame document of a definition the tile validator
// checked without problems, and returns its digest. A digest already held
// is only marked as recently used, so a definition is composed once while it
// stays registered. A new one is checked against its digest before it is
// composed, so a document is never kept under a digest that does not name
// it. It fails for a definition kit.Compose refuses (an unsupported major,
// HTML over kit.MaxAuthorHTML bytes or a document over kit.MaxDocument).
func (r *Registry) Add(def tiles.TypeDefinition) (string, error) {
	digest := def.Digest
	if !digestPattern.MatchString(digest) {
		return "", ErrDigest
	}
	r.mu.Lock()
	if e, ok := r.byKey[digest]; ok {
		r.order.MoveToFront(e)
		r.mu.Unlock()
		return digest, nil
	}
	r.mu.Unlock()
	if def.Source.Format != "html" {
		return "", ErrFormat
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return "", err
	}
	if computed, err := tiles.DefinitionDigest(encoded); err != nil || computed != digest {
		return "", ErrDigest
	}
	document, err := kit.Compose(def.Kit, def.Source.HTML)
	if err != nil {
		return "", err
	}
	Composed.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byKey[digest]; ok {
		// Composed beside another Add of the same definition.
		r.order.MoveToFront(e)
		return digest, nil
	}
	r.byKey[digest] = r.order.PushFront(&frame{digest: digest, document: document})
	r.bytes += len(document)
	for r.bytes > r.max && r.order.Len() > 1 {
		oldest := r.order.Back()
		f := r.order.Remove(oldest).(*frame)
		delete(r.byKey, f.digest)
		r.bytes -= len(f.document)
		r.evicted++
	}
	return digest, nil
}

// Get returns the frame document registered under digest ("sha256:" and 64
// lowercase hex digits), unless it is marked hung. The bytes are shared:
// the caller must not change them.
func (r *Registry) Get(digest string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hung[digest] {
		return nil, false
	}
	e, ok := r.byKey[digest]
	if !ok {
		return nil, false
	}
	r.order.MoveToFront(e)
	return e.Value.(*frame).document, true
}

// MarkHung refuses digest from now on, registered or not, until the daemon
// restarts: a type that froze the app is not served to it again. Marks are
// few (each follows a render check that ran out of time), so they are not
// bounded.
func (r *Registry) MarkHung(digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hung[digest] = true
}

// Hung reports whether digest is marked hung.
func (r *Registry) Hung(digest string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hung[digest]
}

// Stats reports the documents held, their bytes and how many were evicted.
func (r *Registry) Stats() (documents, bytes, evicted int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order.Len(), r.bytes, r.evicted
}
