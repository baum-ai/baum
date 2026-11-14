package tiles

import (
	"errors"
	"fmt"
)

// encoding/json matches an object key to a struct field ignoring case, so a
// key such as "Summary" would decode into the summary and come back from
// ToReview as "summary". Every key the review schema defines is lower case,
// so checkKeyCase rejects, in the objects the converter decodes into structs,
// any key that is not written in lower-case letters, digits and underscores.
// The objects it passes through raw keep their keys as they are.

// typedPaths are the objects the converter decodes into structs, as paths
// from the document root; "*" is any array element.
var typedPaths = [][]string{
	{},
	{"summary"},
	{"sections", "*"},
	{"sections", "*", "risks", "*"},
	{"sections", "*", "evidence", "*"},
	{"sections", "*", "blocks", "*"},
}

// checkKeyCase scans a valid JSON document once.
func checkKeyCase(data []byte) error {
	s := keyScanner{data: data}
	s.value(nil)
	return s.err
}

type keyScanner struct {
	data []byte
	pos  int
	err  error
}

func typedPath(path []string) bool {
	for _, typed := range typedPaths {
		if len(typed) != len(path) {
			continue
		}
		match := true
		for i := range typed {
			if typed[i] != "*" && typed[i] != path[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// relevant reports whether a typed object can lie under path.
func relevant(path []string) bool {
	for _, typed := range typedPaths {
		if len(path) > len(typed) {
			continue
		}
		match := true
		for i := range path {
			if typed[i] != "*" && typed[i] != path[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (s *keyScanner) space() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

// value scans one value at path; a value no typed object can lie under is
// skipped without looking at its keys.
func (s *keyScanner) value(path []string) {
	if s.err != nil {
		return
	}
	s.space()
	if s.pos >= len(s.data) {
		s.err = errors.New("unexpected end of the document")
		return
	}
	if !relevant(path) {
		s.skip()
		return
	}
	switch s.data[s.pos] {
	case '{':
		s.pos++
		typed := typedPath(path)
		for s.err == nil {
			s.space()
			if s.pos < len(s.data) && s.data[s.pos] == '}' {
				s.pos++
				return
			}
			if s.pos < len(s.data) && s.data[s.pos] == ',' {
				s.pos++
				s.space()
			}
			key, plain := s.key()
			if typed && !plain {
				s.err = fmt.Errorf("a key is not written as the review schema writes its keys")
				return
			}
			s.space()
			s.pos++ // the colon
			s.value(append(path[:len(path):len(path)], key))
		}
	case '[':
		s.pos++
		for s.err == nil {
			s.space()
			if s.pos < len(s.data) && s.data[s.pos] == ']' {
				s.pos++
				return
			}
			if s.pos < len(s.data) && s.data[s.pos] == ',' {
				s.pos++
			}
			s.value(append(path[:len(path):len(path)], "*"))
		}
	default:
		s.skip()
	}
}

// key reads an object key and reports whether it is plain lower case.
func (s *keyScanner) key() (string, bool) {
	if s.pos >= len(s.data) || s.data[s.pos] != '"' {
		s.err = errors.New("expected an object key")
		return "", false
	}
	start := s.pos + 1
	plain := true
	for s.pos++; s.pos < len(s.data); s.pos++ {
		c := s.data[s.pos]
		switch {
		case c == '"':
			key := string(s.data[start:s.pos])
			s.pos++
			return key, plain
		case c == '\\':
			plain = false
			s.pos++
		case (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_':
			plain = false
		}
	}
	s.err = errors.New("unterminated key")
	return "", false
}

// skip passes over one value without looking inside it.
func (s *keyScanner) skip() {
	depth := 0
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		switch c {
		case '"':
			for s.pos++; s.pos < len(s.data) && s.data[s.pos] != '"'; s.pos++ {
				if s.data[s.pos] == '\\' {
					s.pos++
				}
			}
			s.pos++
			if depth == 0 {
				return
			}
			continue
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				s.pos++
				return
			}
			if depth < 0 {
				return
			}
		case ',':
			if depth == 0 {
				return
			}
		}
		s.pos++
	}
}
