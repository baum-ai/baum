// Package kit is the custom tile kit: the small plain JavaScript and CSS
// runtime inlined into the frame document of every custom tile, and the
// composition of that document from a tile type's HTML.
//
// A custom tile is one HTML fragment (style, script and optional template
// elements) that draws a tile's data in a sandboxed, opaque-origin iframe. The
// kit gives it the app's look (the viewer's palette, type scale and fonts and
// the app's radii, as generated tokens), an escaping template tag, citation
// chips, diffs and Markdown, and the bootstrap that talks to the page. A tile
// type names the kit major it was written for (`kit: 1`); each major is a
// directory here (v1/) that is kept for good and changes only by addition, so
// a saved tile keeps drawing as it did. Its manifest.json lists every API
// name, element, class and token a tile may use.
//
// See "Custom tile kit" in ARCHITECTURE.md.
package kit

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

//go:embed tokens.css fonts.css v1 GUIDE.md starter
var files embed.FS

// CSP is the Content-Security-Policy a frame document is served with. It
// allows the document's own inline script and styles, data: and blob:
// images and data: fonts, and nothing else: no request leaves the frame.
const CSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; form-action 'none'; base-uri 'none'; " +
	"frame-ancestors 'self'; sandbox allow-scripts"

// Size limits. A type's HTML is at most MaxAuthorHTML bytes and a composed
// frame document at most MaxDocument. A kit major's own scripts and styles
// (its v<n>/ directory as inlined, without the tokens and fonts all majors
// share) are at most MaxKit; a test holds every major to it.
const (
	MaxAuthorHTML = 256 << 10
	MaxDocument   = 512 << 10
	MaxKit        = 64 << 10
)

var majors = []int{1}

// Majors returns the kit majors this build ships, oldest first.
func Majors() []int { return append([]int(nil), majors...) }

// Supported reports whether this build ships kit major n.
func Supported(n int) bool {
	for _, major := range majors {
		if major == n {
			return true
		}
	}
	return false
}

var (
	// ErrUnsupported is a kit major this build does not ship.
	ErrUnsupported = errors.New("kit: unsupported kit major")
	// ErrInvalidHTML is HTML that is not valid UTF-8 or holds a NUL.
	ErrInvalidHTML = errors.New("kit: the tile's HTML must be valid UTF-8 without NUL characters")
	// ErrTooLarge is HTML or a composed document over its limit.
	ErrTooLarge = errors.New("kit: too large")
)

// Guide returns GUIDE.md: how to write a tile type against the kit, for a
// person or a model, with the definition of done.
func Guide() string {
	data, err := files.ReadFile("GUIDE.md")
	if err != nil {
		panic(err)
	}
	return string(data)
}

// Starter returns the starter tile type, a directory in the tile library's
// layout (type.json, fill.md, schema.json, tile.html, samples/) that passes
// every check; a new type starts as a copy of it.
func Starter() fs.FS {
	dir, err := fs.Sub(files, "starter")
	if err != nil {
		panic(err)
	}
	return dir
}

// Manifest is a kit major's manifest.json: the contract a tile is written
// against.
type Manifest struct {
	Kit         int     `json:"kit"`
	Description string  `json:"description"`
	Runtime     Runtime `json:"runtime"`
	API         []Entry `json:"api"`
	View        []Entry `json:"view"`
	Elements    []Entry `json:"elements"`
	Classes     []Entry `json:"classes"`
	Tokens      []Entry `json:"tokens"`
}

// Runtime names the files a frame document inlines, in order.
type Runtime struct {
	// Scripts run in one function scope, in this order.
	Scripts []string `json:"scripts"`
	Styles  []string `json:"styles"`
	// Shared are the generated files every major uses (fonts and tokens).
	Shared []string `json:"shared"`
}

// Entry is one name of the contract.
type Entry struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Description string      `json:"description"`
	Signature   string      `json:"signature,omitempty"`
	CreatedBy   string      `json:"created_by,omitempty"`
	Group       string      `json:"group,omitempty"`
	Attributes  []Attribute `json:"attributes,omitempty"`
}

// Attribute is an attribute an element or class reads.
type Attribute struct {
	Name        string   `json:"name"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description"`
}

// ManifestJSON returns kit major n's manifest.json as shipped.
func ManifestJSON(n int) ([]byte, error) {
	if !Supported(n) {
		return nil, fmt.Errorf("%w: %d", ErrUnsupported, n)
	}
	return files.ReadFile(fmt.Sprintf("v%d/manifest.json", n))
}

// LoadManifest returns kit major n's manifest.
func LoadManifest(n int) (*Manifest, error) {
	data, err := ManifestJSON(n)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("kit: v%d/manifest.json: %w", n, err)
	}
	return &manifest, nil
}

// authorLine is the placeholder the bootstrap holds for the number of lines
// before the author's HTML.
const authorLine = "/*@AUTHOR_LINE@*/ 0"

const suffix = "\n</body>\n</html>\n"

type prelude struct {
	text []byte
	// kit is the size of the major's own scripts and styles as inlined:
	// v<n>/, without the generated tokens and fonts every major shares.
	kit int
	// script is the major's script as inlined, before AUTHOR_LINE is set.
	script string
	err    error
}

var (
	preludesOnce sync.Once
	preludes     map[int]prelude
)

// preludeOf returns the start of kit major n's frame documents, up to where
// the author's HTML begins, and the size of the kit within it (fonts
// excluded). It is the same for every tile of that major.
func preludeOf(n int) prelude {
	preludesOnce.Do(func() {
		preludes = make(map[int]prelude, len(majors))
		for _, major := range majors {
			preludes[major] = buildPrelude(major)
		}
	})
	if p, ok := preludes[n]; ok {
		return p
	}
	return prelude{err: fmt.Errorf("%w: %d", ErrUnsupported, n)}
}

func buildPrelude(n int) prelude {
	text, size, script, err := assemble(n)
	return prelude{text: text, kit: size, script: script, err: err}
}

func assemble(n int) ([]byte, int, string, error) {
	manifest, err := LoadManifest(n)
	if err != nil {
		return nil, 0, "", err
	}
	dir := fmt.Sprintf("v%d", n)
	read := func(name string) (string, error) {
		data, err := files.ReadFile(path.Join(dir, name))
		return string(data), err
	}
	tokens, err := files.ReadFile("tokens.css")
	if err != nil {
		return nil, 0, "", err
	}
	fonts, err := files.ReadFile("fonts.css")
	if err != nil {
		return nil, 0, "", err
	}
	var style strings.Builder
	style.WriteString(compactCSS(string(tokens)))
	own := 0
	for _, name := range manifest.Runtime.Styles {
		css, err := read(name)
		if err != nil {
			return nil, 0, "", err
		}
		css = compactCSS(css)
		own += len(css)
		style.WriteString("\n")
		style.WriteString(css)
	}
	var script strings.Builder
	script.WriteString("(function () {\n\"use strict\";\n")
	for _, name := range manifest.Runtime.Scripts {
		js, err := read(name)
		if err != nil {
			return nil, 0, "", err
		}
		js, err = compactJS(js)
		if err != nil {
			return nil, 0, "", fmt.Errorf("kit: v%d/%s: %w", n, name, err)
		}
		script.WriteString(js)
		script.WriteString("\n")
	}
	script.WriteString("})();")
	if strings.Count(script.String(), authorLine) != 1 {
		return nil, 0, "", fmt.Errorf("kit: v%d's scripts must hold %s exactly once", n, authorLine)
	}
	head := "<!doctype html>\n" +
		"<html lang=\"en\" data-theme=\"light\" data-face=\"\">\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n" +
		"<style id=\"baum-kit-style\">" + string(fonts) + style.String() + "</style>\n" +
		"<script id=\"baum-kit-script\">" + script.String() + "</script>\n" +
		"</head>\n" +
		"<body><div id=\"baum-root\"></div>\n"
	// The author's first line is the line after the prelude's last newline.
	lines := strings.Count(head, "\n")
	head = strings.Replace(head, authorLine, strconv.Itoa(lines), 1)
	return []byte(head), own + script.Len(), script.String(), nil
}

// compactJS drops a kit script's comment lines, indentation and blank
// lines. Every line of code is kept whole, so nothing a statement means can
// change; a block comment must start and end its own lines.
func compactJS(js string) (string, error) {
	var out []string
	comment := false
	for _, line := range strings.Split(js, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case comment:
			if strings.Contains(line, "*/") {
				if !strings.HasSuffix(line, "*/") {
					return "", errors.New("code after a block comment on its line")
				}
				comment = false
			}
		case strings.HasPrefix(line, "/*"):
			if !strings.Contains(line, "*/") {
				comment = true
			} else if !strings.HasSuffix(line, "*/") {
				out = append(out, line)
			}
		case line == "" || strings.HasPrefix(line, "//"):
		default:
			squeezed, err := squeeze(line)
			if err != nil {
				return "", err
			}
			out = append(out, squeezed)
		}
	}
	return strings.Join(out, "\n"), nil
}

// squeeze drops the spaces of a line of code that separate nothing: those
// outside string and template literals with punctuation on either side. A
// line holding a slash (a regular expression, a division or a comment) is
// kept as it is, and so is a space between two names (a character beyond
// ASCII counts as part of a name) or between two plus or minus signs. A
// literal must end on its line.
func squeeze(line string) (string, error) {
	if strings.Contains(line, "/") {
		return line, nil
	}
	name := func(c byte) bool {
		return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		if c == '"' || c == '\'' || c == '`' {
			end, whole := literalEnd(line, i)
			if end < 0 {
				return "", fmt.Errorf("a literal runs past its line: %s", line)
			}
			if !whole {
				// A template expression holding a literal or a template of
				// its own: keep the rest of the line as it is.
				b.WriteString(line[i:])
				return b.String(), nil
			}
			b.WriteString(line[i:end])
			i = end
			continue
		}
		if c != ' ' {
			b.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == ' ' {
			j++
		}
		var before, after byte
		if b.Len() > 0 {
			before = b.String()[b.Len()-1]
		}
		if j < len(line) {
			after = line[j]
		}
		signs := strings.IndexByte("+-", before) >= 0 && strings.IndexByte("+-", after) >= 0
		if before != 0 && after != 0 && (name(before) && name(after) || signs) {
			b.WriteByte(' ')
		}
		i = j
	}
	return b.String(), nil
}

// literalEnd returns the offset after the string or template literal that
// starts at line[i], or -1 when it does not end on the line. whole is false
// for a template whose expressions hold a quote or a backtick, which this
// scan does not follow.
func literalEnd(line string, i int) (end int, whole bool) {
	quote := line[i]
	depth := 0
	for j := i + 1; j < len(line); j++ {
		c := line[j]
		switch {
		case c == '\\':
			j++
		case depth == 0 && c == quote:
			return j + 1, true
		case quote == '`' && depth == 0 && c == '$' && j+1 < len(line) && line[j+1] == '{':
			depth = 1
			j++
		case depth > 0 && c == '{':
			depth++
		case depth > 0 && c == '}':
			depth--
		case depth > 0 && (c == '"' || c == '\'' || c == '`'):
			return len(line), false
		}
	}
	return -1, false
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// compactCSS drops comments, indentation and blank lines, joins a line to
// the one before when that ends a selector, declaration or rule, and drops
// the space after a colon or comma and before a brace. The kit's styles hold
// no strings or selectors these change.
func compactCSS(css string) string {
	var b strings.Builder
	for _, line := range strings.Split(cssComment.ReplaceAllString(css, ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if b.Len() > 0 && !strings.ContainsAny(b.String()[b.Len()-1:], "{};,") {
			b.WriteByte('\n')
		}
		b.WriteString(cssSpaces.Replace(line))
	}
	return b.String()
}

var cssSpaces = strings.NewReplacer(": ", ":", ", ", ",", " {", "{")

// Compose returns the frame document for a tile type written for kit major
// n with the given HTML. The document is the prelude (doctype, charset,
// viewport, the kit's styles, then the kit's script, which installs its
// listeners and defines window.baum before any author code runs), an empty
// #baum-root, the author's HTML exactly as written (less one leading byte
// order mark) and the closing tags.
//
// The HTML is not parsed or sanitised: the frame's sandbox and CSP are the
// boundary, and Lint reports honest mistakes. Stray structure lands where
// the HTML parser puts it, after the kit, which it cannot affect: a doctype,
// <html>, <head> or <body> inside the body is ignored (or only adds
// attributes the element lacks), a <meta http-equiv> policy outside <head>
// is ignored, a <base> is refused by base-uri 'none', a refresh or any other
// navigation is refused by the page's frame-src and the daemon's same-origin
// rule, and an unclosed element or script only swallows the closing tags.
//
// It fails for an unsupported major, HTML that is not valid UTF-8 or holds a
// NUL, HTML over MaxAuthorHTML and a document over MaxDocument.
func Compose(n int, html string) ([]byte, error) {
	p := preludeOf(n)
	if p.err != nil {
		return nil, p.err
	}
	head := p.text
	if !utf8.ValidString(html) || strings.ContainsRune(html, 0) {
		return nil, ErrInvalidHTML
	}
	html = strings.TrimPrefix(html, "\uFEFF")
	if len(html) > MaxAuthorHTML {
		return nil, fmt.Errorf("%w: the tile's HTML is %d bytes, over %d", ErrTooLarge, len(html), MaxAuthorHTML)
	}
	var document bytes.Buffer
	document.Grow(len(head) + len(html) + len(suffix))
	document.Write(head)
	document.WriteString(html)
	document.WriteString(suffix)
	if document.Len() > MaxDocument {
		return nil, fmt.Errorf("%w: the frame document is %d bytes, over %d", ErrTooLarge, document.Len(), MaxDocument)
	}
	return document.Bytes(), nil
}
