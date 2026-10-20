package userhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// The settings files are edited as text: a change splices the bytes it adds
// or removes and leaves everything else, key order and formatting included,
// exactly as the user or the agent wrote it. jsonValue is a parsed value
// that remembers where its text sits in the document.

type jsonKind int

const (
	jsonObject jsonKind = iota
	jsonArray
	jsonString
	jsonScalar
)

type jsonValue struct {
	kind       jsonKind
	start, end int
	members    []jsonMember // jsonObject
	elements   []*jsonValue // jsonArray
}

type jsonMember struct {
	key   string
	start int // the opening quote of the key
	value *jsonValue
}

// span is the text of one object member or array element.
type span struct{ start, end int }

func (v *jsonValue) items() []span {
	switch v.kind {
	case jsonObject:
		items := make([]span, len(v.members))
		for i, member := range v.members {
			items[i] = span{member.start, member.value.end}
		}
		return items
	case jsonArray:
		items := make([]span, len(v.elements))
		for i, element := range v.elements {
			items[i] = span{element.start, element.end}
		}
		return items
	}
	return nil
}

// member returns the value of key, refusing a key that appears twice: which
// copy an agent honours is its own business, so neither is safe to edit.
func (v *jsonValue) member(key string) (*jsonValue, int, error) {
	var found *jsonValue
	index := -1
	for i, member := range v.members {
		if member.key != key {
			continue
		}
		if found != nil {
			return nil, -1, errors.New("duplicate key " + quote(key))
		}
		found, index = member.value, i
	}
	return found, index, nil
}

func (v *jsonValue) text(document []byte) []byte { return document[v.start:v.end] }

// stringValue decodes a JSON string value; ok is false for any other kind.
func (v *jsonValue) stringValue(document []byte) (string, bool) {
	if v == nil || v.kind != jsonString {
		return "", false
	}
	var value string
	if err := json.Unmarshal(v.text(document), &value); err != nil {
		return "", false
	}
	return value, true
}

func parseJSON(document []byte) (*jsonValue, error) {
	if !json.Valid(document) {
		return nil, errors.New("not valid JSON")
	}
	p := jsonParser{document: document}
	p.space()
	return p.value(), nil
}

// jsonParser walks a document json.Valid has accepted, so it records
// positions without reporting syntax errors of its own.
type jsonParser struct {
	document []byte
	at       int
}

func (p *jsonParser) space() {
	for p.at < len(p.document) && isSpace(p.document[p.at]) {
		p.at++
	}
}

func (p *jsonParser) value() *jsonValue {
	value := &jsonValue{start: p.at}
	switch p.document[p.at] {
	case '{':
		value.kind = jsonObject
		p.at++
		p.space()
		for p.document[p.at] != '}' {
			keyStart := p.at
			p.skipString()
			var key string
			_ = json.Unmarshal(p.document[keyStart:p.at], &key)
			p.space()
			p.at++ // :
			p.space()
			value.members = append(value.members, jsonMember{key: key, start: keyStart, value: p.value()})
			p.space()
			if p.document[p.at] == ',' {
				p.at++
				p.space()
			}
		}
		p.at++
	case '[':
		value.kind = jsonArray
		p.at++
		p.space()
		for p.document[p.at] != ']' {
			value.elements = append(value.elements, p.value())
			p.space()
			if p.document[p.at] == ',' {
				p.at++
				p.space()
			}
		}
		p.at++
	case '"':
		value.kind = jsonString
		p.skipString()
	default:
		value.kind = jsonScalar
		for p.at < len(p.document) && !isSpace(p.document[p.at]) && !strings.ContainsRune(",]}", rune(p.document[p.at])) {
			p.at++
		}
	}
	value.end = p.at
	return value
}

func (p *jsonParser) skipString() {
	for p.at++; p.at < len(p.document); p.at++ {
		switch p.document[p.at] {
		case '\\':
			p.at++
		case '"':
			p.at++
			return
		}
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// edit replaces document[from:to] with text.
type edit struct {
	from, to int
	text     string
}

func (e edit) apply(document []byte) []byte {
	out := make([]byte, 0, len(document)-(e.to-e.from)+len(e.text))
	out = append(out, document[:e.from]...)
	out = append(out, e.text...)
	return append(out, document[e.to:]...)
}

// removeItem cuts one member or element out of a container together with the
// separator before it (or, for the first item, after it), which is exactly
// what appendItem adds, so an append followed by a removal restores the bytes.
func removeItem(container *jsonValue, index int) edit {
	items := container.items()
	switch {
	case len(items) == 1:
		return edit{container.start + 1, container.end - 1, ""}
	case index > 0:
		return edit{items[index-1].end, items[index].end, ""}
	default:
		return edit{items[0].start, items[1].start, ""}
	}
}

// layout is how a document is indented, by unit per level or on one line,
// and how its lines end: a file written on Windows keeps its CRLFs.
type layout struct {
	unit    string
	compact bool
	newline string
}

func detectLayout(document []byte) layout {
	style := detectIndent(document)
	style.newline = "\n"
	if bytes.Contains(document, []byte("\r\n")) {
		style.newline = "\r\n"
	}
	return style
}

func detectIndent(document []byte) layout {
	if !bytes.ContainsRune(bytes.TrimSpace(document), '\n') {
		// A one-line document stays on one line, unless it is the empty
		// object a new or blank file starts as.
		if string(bytes.TrimSpace(document)) == "{}" {
			return layout{unit: "  "}
		}
		return layout{compact: true}
	}
	for _, line := range bytes.Split(document, []byte("\n"))[1:] {
		indent := line[:len(line)-len(bytes.TrimLeft(line, " \t"))]
		if len(indent) > 0 && len(indent) < len(line) {
			return layout{unit: string(indent)}
		}
	}
	return layout{unit: "  "}
}

// lineIndent is the leading whitespace of the line holding pos.
func lineIndent(document []byte, pos int) string {
	start := bytes.LastIndexByte(document[:pos], '\n') + 1
	end := start
	for end < pos && (document[end] == ' ' || document[end] == '\t') {
		end++
	}
	return string(document[start:end])
}

// appendItem adds an item after the container's last one, separated and
// indented like its first. render gets the indentation of the item's first
// line, which nested lines extend by the document's unit.
func appendItem(document []byte, container *jsonValue, depthIndent string, style layout, render func(indent string) (string, error)) (edit, error) {
	items := container.items()
	if len(items) == 0 {
		if style.compact {
			text, err := render("")
			return edit{container.start + 1, container.end - 1, text}, err
		}
		inner := depthIndent + style.unit
		text, err := render(inner)
		return edit{container.start + 1, container.end - 1, style.newline + inner + text + style.newline + depthIndent}, err
	}
	gap := string(document[container.start+1 : items[0].start])
	text, err := render(gap[strings.LastIndexByte(gap, '\n')+1:])
	return edit{items[len(items)-1].end, items[len(items)-1].end, "," + gap + text}, err
}

// encode renders a value on lines indented from indent, or on one line.
func encode(value any, indent string, style layout) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if !style.compact {
		encoder.SetIndent(indent, style.unit)
	}
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	text := strings.TrimSuffix(buffer.String(), "\n")
	if style.newline != "" && style.newline != "\n" {
		text = strings.ReplaceAll(text, "\n", style.newline)
	}
	return text, nil
}

func encodeMember(key string, value any, indent string, style layout) (string, error) {
	text, err := encode(value, indent, style)
	if err != nil {
		return "", err
	}
	separator := ": "
	if style.compact {
		separator = ":"
	}
	return quote(key) + separator + text, nil
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
