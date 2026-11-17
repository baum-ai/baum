package tiles

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/review"
)

// A baum.review/1 risk, evidence or decision ID only has to be unique within
// its section, but a tile document's must be unique across the document, so
// tiles and chips can name an item by its ID alone. The first occurrence in
// document order keeps its ID; each later one is renamed to its ID and its
// section's and records the original in its origin. Decisions are renamed
// only among current sections, whose decisions the decisions tile holds; an
// obsolete section keeps its blocks, and its decisions their IDs.
//
// A section's prose and structured references (attention refs, behaviour
// step evidence) that name a renamed ID are rewritten to the new one within
// that section only, where they meant the section's own item: the first item
// of the section with that original ID. A name is rewritten as a whole token,
// as the viewer links it (itemToken), but never in an item's own ID, a path,
// a URL, an inline code span or a verbatim quote (a decision's rejected
// alternative and evidence, a prompt anchor's input). A new ID never occurs
// in any string of the source document, so ToReview can rewrite it back
// exactly.

// itemToken matches the item IDs the viewer links in prose
// (oss/viewer/src/components/markdown.tsx).
var itemToken = regexp.MustCompile(`\b(?:rsk|ev|dec)_[a-z0-9_]+\b`)

// urlPattern matches a URL in prose, up to the first character that ends
// one. Rewriting a token outside a URL adds no such character and no scheme,
// so the same spans are found in the rewritten text.
var urlPattern = regexp.MustCompile("https?://[^\\s<>()\\[\\]\"'`]+")

// untouchedKeys are the object keys whose string values are never rewritten:
// an item's own ID is restored from its origin, and paths and URLs are not
// prose.
var untouchedKeys = map[string]bool{"id": true, "path": true, "url": true}

// quoteKeys are the keys whose string value is a verbatim quote. A
// behaviour step's "evidence" is an array of references, which is
// rewritten; a decision's "evidence" is a quoted string, which is not.
var quoteKeys = map[string]bool{"evidence": true, "rejected": true, "input": true}

// renamer assigns new IDs to repeated ones, per ID prefix.
type renamer struct {
	source *sourceText
	used   map[string]bool
	seen   map[string]bool
}

// sourceText is every string value of the source document, decoded once,
// when the first ID needs a new name.
type sourceText struct {
	raw   []byte
	text  string
	ready bool
}

func (s *sourceText) contains(name string) bool {
	if !s.ready {
		s.ready = true
		decoder := json.NewDecoder(bytes.NewReader(s.raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			var b strings.Builder
			collectStrings(value, &b)
			s.text = b.String()
		} else {
			s.text = string(s.raw)
		}
	}
	return strings.Contains(s.text, name)
}

func collectStrings(value any, b *strings.Builder) {
	switch v := value.(type) {
	case map[string]any:
		for _, child := range v {
			collectStrings(child, b)
		}
	case []any:
		for _, child := range v {
			collectStrings(child, b)
		}
	case string:
		b.WriteString(v)
		b.WriteByte(0)
	}
}

func newRenamer(source *sourceText, ids []string) *renamer {
	used := make(map[string]bool, len(ids))
	for _, id := range ids {
		used[id] = true
	}
	return &renamer{source: source, used: used, seen: map[string]bool{}}
}

// next returns the ID the item with this ID in this section gets: its own the
// first time the ID occurs, a new one after that.
func (r *renamer) next(id, section string) string {
	if !r.seen[id] {
		r.seen[id] = true
		return id
	}
	base := id + "_" + strings.TrimPrefix(section, "sec_")
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate += "_" + strconv.Itoa(n)
		}
		candidate = review.ShortenIdentifier(candidate)
		if !r.used[candidate] && !r.source.contains(candidate) {
			r.used[candidate] = true
			return candidate
		}
	}
}

// sectionRefs maps, within one section, an original item ID to the ID its
// references are rewritten to.
type sectionRefs map[string]string

// add records an item of the section: the first item with an original ID
// decides where references to it point.
func (refs sectionRefs) add(original, id string, seen map[string]bool) {
	if seen[original] {
		return
	}
	seen[original] = true
	if original != id {
		refs[original] = id
	}
}

func (refs sectionRefs) inverse() sectionRefs {
	inverse := make(sectionRefs, len(refs))
	for from, to := range refs {
		inverse[to] = from
	}
	return inverse
}

// rewrite returns raw with every reference refs names rewritten. It returns
// raw itself when nothing in it can match.
func (refs sectionRefs) rewrite(raw json.RawMessage) (json.RawMessage, error) {
	if len(refs) == 0 || !refs.mentioned(raw) {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(refs.rewriteValue(value, ""))
}

func (refs sectionRefs) mentioned(raw []byte) bool {
	for from := range refs {
		if bytes.Contains(raw, []byte(from)) {
			return true
		}
	}
	return false
}

func (refs sectionRefs) rewriteValue(value any, key string) any {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = refs.rewriteValue(child, k)
		}
	case []any:
		for i := range v {
			if s, ok := v[i].(string); ok {
				if !untouchedKeys[key] {
					v[i] = refs.rewriteText(s)
				}
				continue
			}
			v[i] = refs.rewriteValue(v[i], key)
		}
	case string:
		if untouchedKeys[key] || quoteKeys[key] {
			return v
		}
		return refs.rewriteText(v)
	}
	return value
}

// rewriteText rewrites the references in prose outside its inline code spans
// and URLs. Rewriting never adds or removes a backtick, so the spans are the
// same in the rewritten text.
func (refs sectionRefs) rewriteText(text string) string {
	if !itemToken.MatchString(text) {
		return text
	}
	parts := strings.Split(text, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = refs.rewriteOutsideURLs(parts[i])
	}
	return strings.Join(parts, "`")
}

func (refs sectionRefs) rewriteOutsideURLs(text string) string {
	replace := func(prose string) string {
		return itemToken.ReplaceAllStringFunc(prose, func(token string) string {
			if to, ok := refs[token]; ok {
				return to
			}
			return token
		})
	}
	urls := urlPattern.FindAllStringIndex(text, -1)
	if urls == nil {
		return replace(text)
	}
	var b strings.Builder
	last := 0
	for _, span := range urls {
		b.WriteString(replace(text[last:span[0]]))
		b.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	b.WriteString(replace(text[last:]))
	return b.String()
}

// rewriteAll rewrites each raw value in place.
func (refs sectionRefs) rewriteAll(values []json.RawMessage) error {
	for i, value := range values {
		rewritten, err := refs.rewrite(value)
		if err != nil {
			return err
		}
		values[i] = rewritten
	}
	return nil
}
