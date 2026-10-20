package baumcli

import "encoding/json"

// Bounds of what topLevelStrings keeps.
const (
	maxPayloadKey   = 64
	maxPayloadValue = 4096
)

// topLevelStrings reads a JSON object as it streams by and keeps the string
// members of its top level whose names it is given, each up to
// maxPayloadValue bytes. It keeps nothing else, however large the object or
// its other members are, so an agent's payload of any size yields its
// identifiers in bounded memory. A name given twice keeps its last value, as
// json.Unmarshal does. Input that is not an object yields nothing; a value
// too long, or not a string, is skipped; a payload cut short yields the
// members read before the cut.
type topLevelStrings struct {
	keep   map[string]bool
	values map[string]string

	started, stopped bool
	depth            int
	expectKey        bool
	inString, escape bool
	// capture is where the current string's raw bytes go: the member name,
	// a kept value, or nowhere.
	capture          *[]byte
	limit            int
	overflow         bool
	key, keyBuf, val []byte
}

func newTopLevelStrings(names ...string) *topLevelStrings {
	keep := make(map[string]bool, len(names))
	for _, name := range names {
		keep[name] = true
	}
	return &topLevelStrings{keep: keep, values: map[string]string{}}
}

// Write consumes the next bytes of the stream. It never fails.
func (t *topLevelStrings) Write(p []byte) (int, error) {
	for _, c := range p {
		if t.stopped {
			break
		}
		t.next(c)
	}
	return len(p), nil
}

func (t *topLevelStrings) next(c byte) {
	if t.inString {
		switch {
		case t.escape:
			t.escape = false
		case c == '\\':
			t.escape = true
		case c == '"':
			t.inString = false
			t.endString()
			return
		}
		if t.capture != nil {
			if len(*t.capture) < t.limit {
				*t.capture = append(*t.capture, c)
			} else {
				t.overflow = true
			}
		}
		return
	}
	switch c {
	case ' ', '\t', '\r', '\n':
	case '{', '[':
		if !t.started {
			t.started = true
			if c != '{' {
				t.stopped = true
				return
			}
			t.expectKey = true
		}
		t.depth++
	case '}', ']':
		t.depth--
		if t.depth <= 0 {
			t.stopped = true
		}
	case ':':
		if t.depth == 1 {
			t.expectKey = false
		}
	case ',':
		if t.depth == 1 {
			t.expectKey = true
		}
	case '"':
		if !t.started {
			t.stopped = true
			return
		}
		t.inString, t.overflow, t.capture = true, false, nil
		switch {
		case t.depth == 1 && t.expectKey:
			t.keyBuf, t.capture, t.limit = t.keyBuf[:0], &t.keyBuf, maxPayloadKey
		case t.depth == 1 && t.keep[string(t.key)]:
			t.val, t.capture, t.limit = t.val[:0], &t.val, maxPayloadValue
		}
	default:
		if !t.started {
			t.stopped = true
		}
	}
}

// endString files the string just read: a member name becomes the current
// key, a kept value is decoded, unless it ran over its bound.
func (t *topLevelStrings) endString() {
	switch t.capture {
	case &t.keyBuf:
		t.key = t.key[:0]
		if !t.overflow {
			if name, ok := decodeJSONString(t.keyBuf); ok {
				t.key = append(t.key, name...)
			}
		}
	case &t.val:
		if value, ok := decodeJSONString(t.val); ok && !t.overflow {
			t.values[string(t.key)] = value
		}
	}
	t.capture = nil
}

func decodeJSONString(raw []byte) (string, bool) {
	quoted := make([]byte, 0, len(raw)+2)
	quoted = append(append(append(quoted, '"'), raw...), '"')
	var value string
	if json.Unmarshal(quoted, &value) != nil {
		return "", false
	}
	return value, true
}
