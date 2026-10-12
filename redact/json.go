package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// RedactJSON masks the secrets and e-mail addresses in data, a JSON text, and
// returns valid JSON of the same shape whenever data is valid.
//
// It finds them two ways and masks the union:
//
//   - over the encoded text as a whole, as RedactContext would, so a key and
//     its value in separate strings ("password": "…") are caught together;
//   - over every string value and object key as decoded, so a rule anchored
//     at the start of a line still fires after an escaped newline or tab (a
//     tool's output holding an env file). These are scanned together, one
//     string per line, in one pass.
//
// Only the characters of strings are ever masked: never a quote, an escape's
// backslash, or anything between strings. A string with something masked is
// written again as JSON, with no HTML escaping; any other is left byte for
// byte as it was. The Summary adds up the findings of both passes, so a
// secret may be counted twice.
func (e *Engine) RedactJSON(ctx context.Context, data []byte) ([]byte, Summary, error) {
	literals, err := scanStrings(data)
	if err != nil {
		return nil, Summary{}, err
	}
	whole, summary, err := e.RedactContext(ctx, string(data))
	if err != nil {
		return nil, Summary{}, err
	}
	if len(whole) != len(data) {
		return nil, Summary{}, errors.New("redact: the mask changed the length of the JSON text")
	}
	values := make([]string, len(literals))
	for i, lit := range literals {
		values[i] = lit.value
	}
	joined := strings.Join(values, "\n")
	each, eachSummary, err := e.RedactContext(ctx, joined)
	if err != nil {
		return nil, Summary{}, err
	}
	if len(each) != len(joined) {
		return nil, Summary{}, errors.New("redact: the mask changed the length of the JSON strings")
	}
	summary.Add(eachSummary)

	var out bytes.Buffer
	out.Grow(len(data))
	last, offset := 0, 0
	for _, lit := range literals {
		masked := []byte(each[offset : offset+len(lit.value)])
		offset += len(lit.value) + 1
		changed := false
		for j := range masked {
			switch {
			case masked[j] != lit.value[j]:
				changed = true
			case lit.raw[j] >= 0 && whole[lit.raw[j]] != data[lit.raw[j]]:
				masked[j] = whole[lit.raw[j]]
				changed = true
			}
		}
		if !changed {
			continue
		}
		out.Write(data[last:lit.start])
		if err := writeString(&out, string(masked)); err != nil {
			return nil, Summary{}, err
		}
		last = lit.end
	}
	if last == 0 {
		return append([]byte(nil), data...), summary, nil
	}
	out.Write(data[last:])
	return out.Bytes(), summary, nil
}

// literal is one JSON string in a text: its bytes [start, end), quotes
// included, its decoded value, and for each byte of the value the offset of
// the byte in the text it was copied from, or -1 for a byte an escape
// produced.
type literal struct {
	start, end int
	value      string
	raw        []int
}

// scanStrings finds and decodes every string of a JSON text, object keys
// included. It reads only as much of the grammar as strings need: a quote
// outside a string opens one.
func scanStrings(data []byte) ([]literal, error) {
	var literals []literal
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		lit := literal{start: i}
		var value []byte
		closed := false
		for i++; i < len(data); i++ {
			b := data[i]
			if b == '"' {
				closed = true
				break
			}
			if b != '\\' {
				value = append(value, b)
				lit.raw = append(lit.raw, i)
				continue
			}
			if i+1 >= len(data) {
				return nil, errors.New("redact: an escape ends the JSON text")
			}
			r, next, err := unescape(data, i)
			if err != nil {
				return nil, err
			}
			i = next - 1
			encoded := utf8.AppendRune(nil, r)
			value = append(value, encoded...)
			for range encoded {
				lit.raw = append(lit.raw, -1)
			}
		}
		if !closed {
			return nil, errors.New("redact: a JSON string is not closed")
		}
		lit.end = i + 1
		lit.value = string(value)
		literals = append(literals, lit)
	}
	return literals, nil
}

// unescape decodes the escape at data[at] (a backslash) and returns the
// rune and the offset after it. A lone surrogate reads as U+FFFD, as
// encoding/json reads it.
func unescape(data []byte, at int) (rune, int, error) {
	switch esc := data[at+1]; esc {
	case '"', '\\', '/':
		return rune(esc), at + 2, nil
	case 'b':
		return '\b', at + 2, nil
	case 'f':
		return '\f', at + 2, nil
	case 'n':
		return '\n', at + 2, nil
	case 'r':
		return '\r', at + 2, nil
	case 't':
		return '\t', at + 2, nil
	case 'u':
		code, ok := hex4(data, at+2)
		if !ok {
			return 0, 0, errors.New("redact: a \\u escape without four hex digits")
		}
		r, next := rune(code), at+6
		if !utf16.IsSurrogate(r) {
			return r, next, nil
		}
		if next+1 < len(data) && data[next] == '\\' && data[next+1] == 'u' {
			if low, ok := hex4(data, next+2); ok {
				if pair := utf16.DecodeRune(r, rune(low)); pair != utf8.RuneError {
					return pair, next + 6, nil
				}
			}
		}
		return utf8.RuneError, next, nil
	}
	return 0, 0, errors.New("redact: an unknown escape in a JSON string")
}

// hex4 reads four hex digits at data[at:].
func hex4(data []byte, at int) (uint16, bool) {
	if at+4 > len(data) {
		return 0, false
	}
	var v uint16
	for _, c := range data[at : at+4] {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | uint16(d)
	}
	return v, true
}

// writeString writes s as a JSON string without HTML escaping.
func writeString(out *bytes.Buffer, s string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	out.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return nil
}
