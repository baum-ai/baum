package kit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/tiles"
)

const sampleTile = `<style>
  .count { color: var(--text-secondary); }
</style>
<script>
  baum.tile({
    glance: ({ data }) => baum.html` + "`" + `<p class="kit-lede">${data.title}</p>` + "`" + `,
    detail: ({ data }) => baum.md(data.text),
  });
</script>
`

// kitParts returns the kit's style and script text of major n as Compose
// inlines them.
func kitParts(t *testing.T, n int) (style, script string) {
	t.Helper()
	p := preludeOf(n)
	if p.err != nil {
		t.Fatal(p.err)
	}
	text := string(p.text)
	style = between(t, text, `<style id="baum-kit-style">`, "</style>")
	script = between(t, text, `<script id="baum-kit-script">`, "</script>")
	return style, script
}

func between(t *testing.T, text, open, close string) string {
	t.Helper()
	start := strings.Index(text, open)
	if start < 0 {
		t.Fatalf("no %q", open)
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		t.Fatalf("no %q after %q", close, open)
	}
	return text[start : start+end]
}

// skeleton is a composed document with the kit's own text elided, for a
// golden a reader can review.
func skeleton(t *testing.T, document []byte) string {
	style, script := kitParts(t, 1)
	text := string(document)
	text = strings.Replace(text, style, "{fonts.css, tokens.css, v1/kit.css}", 1)
	return strings.Replace(text, script, "{v1 scripts}", 1)
}

func TestComposeGolden(t *testing.T) {
	document, err := Compose(1, sampleTile)
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Text(t, "testdata/compose.golden", skeleton(t, document))
}

func TestComposeAuthorLine(t *testing.T) {
	document, err := Compose(1, "<script>\nthrow new Error('x');\n</script>")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`const AUTHOR_LINE = (\d+);`).FindSubmatch(document)
	if match == nil {
		t.Fatal("the bootstrap's AUTHOR_LINE was not set")
	}
	authorLine, _ := strconv.Atoi(string(match[1]))
	lines := strings.Split(string(document), "\n")
	if got := lines[authorLine]; got != "<script>" {
		t.Errorf("document line %d (the author's line 1) is %q, want the author's first line", authorLine+1, got)
	}
	if got := lines[authorLine+1]; got != "throw new Error('x');" {
		t.Errorf("document line %d (the author's line 2) is %q", authorLine+2, got)
	}
}

// parsed walks a composed document as a browser's parser builds it.
type parsed struct {
	head, body *html.Node
	scripts    []*html.Node
	styles     []*html.Node
}

func parse(t *testing.T, document []byte) parsed {
	t.Helper()
	root, err := html.Parse(strings.NewReader(string(document)))
	if err != nil {
		t.Fatal(err)
	}
	var p parsed
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.DataAtom {
			case atom.Head:
				p.head = node
			case atom.Body:
				p.body = node
			case atom.Script:
				p.scripts = append(p.scripts, node)
			case atom.Style:
				p.styles = append(p.styles, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return p
}

func text(node *html.Node) string {
	var b strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			b.WriteString(child.Data)
		}
	}
	return b.String()
}

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// Whatever the author writes, the parser keeps the kit's style and script
// whole and first, in <head>, and #baum-root first in <body>.
func TestComposeHostileHTML(t *testing.T) {
	style, script := kitParts(t, 1)
	cases := map[string]string{
		"unclosed script":          "<script>baum.tile({glance(){}, detail(){}});\n",
		"closing body in a string": `<script>const s = "</body></html>"; baum.tile({glance(){}, detail(){}});</script>`,
		"base":                     `<base href="https://example.invalid/"><script>baum.tile({glance(){}, detail(){}});</script>`,
		"own document":             `<!doctype html><html lang="fr" class="x"><head><meta http-equiv="Content-Security-Policy" content="default-src *"><title>t</title></head><body><script>1</script></body></html>`,
		"meta refresh":             `<meta http-equiv="refresh" content="0;url=https://example.invalid/">`,
		"unclosed comment":         "<!-- never closed\n<script>1</script>",
		"plaintext":                "<plaintext><script>1</script>",
		"closing head":             "</head><script>1</script>",
		"unclosed attribute":       `<div title="`,
		"style breakout":           "<style></style><style>body{}</style></head><script>1</script>",
	}
	for name, author := range cases {
		t.Run(name, func(t *testing.T) {
			document, err := Compose(1, author)
			if err != nil {
				t.Fatal(err)
			}
			p := parse(t, document)
			if len(p.styles) == 0 || attr(p.styles[0], "id") != "baum-kit-style" || text(p.styles[0]) != style {
				t.Fatal("the kit's style is not the first style, whole")
			}
			if len(p.scripts) == 0 || attr(p.scripts[0], "id") != "baum-kit-script" || text(p.scripts[0]) != script {
				t.Fatal("the kit's script is not the first script, whole")
			}
			if p.scripts[0].Parent != p.head || p.styles[0].Parent != p.head {
				t.Error("the kit is not in <head>")
			}
			first := p.body.FirstChild
			if first == nil || first.DataAtom != atom.Div || attr(first, "id") != "baum-root" {
				t.Error("#baum-root is not the body's first child")
			}
			if !strings.HasPrefix(string(document), "<!doctype html>\n<html lang=\"en\" data-theme=\"light\" data-face=\"\">") {
				t.Error("the document does not start with the kit's doctype and root")
			}
			if !strings.Contains(string(document), "<div id=\"baum-root\"></div>\n"+author+"\n</body>\n</html>\n") {
				t.Error("the author's HTML is not placed verbatim after #baum-root")
			}
		})
	}
}

func TestComposeByteOrderMark(t *testing.T) {
	with, err := Compose(1, "\uFEFF<script>1</script>")
	if err != nil {
		t.Fatal(err)
	}
	without, err := Compose(1, "<script>1</script>")
	if err != nil {
		t.Fatal(err)
	}
	if string(with) != string(without) {
		t.Error("a leading byte order mark was kept")
	}
}

func TestComposeRefuses(t *testing.T) {
	cases := []struct {
		name  string
		major int
		html  string
		want  error
	}{
		{"invalid UTF-8", 1, "<p>\xff\xfe</p>", ErrInvalidHTML},
		{"NUL", 1, "<p>a\x00b</p>", ErrInvalidHTML},
		{"too large", 1, strings.Repeat("a", MaxAuthorHTML+1), ErrTooLarge},
		{"unsupported major", 2, "<p></p>", ErrUnsupported},
		{"major zero", 0, "<p></p>", ErrUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compose(tc.major, tc.html); !errors.Is(err, tc.want) {
				t.Errorf("Compose = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBudgets(t *testing.T) {
	for _, major := range Majors() {
		p := preludeOf(major)
		if p.err != nil {
			t.Fatal(p.err)
		}
		fonts, _ := files.ReadFile("fonts.css")
		tokens, _ := files.ReadFile("tokens.css")
		t.Logf("kit v%d: %d bytes of scripts and styles as inlined; shared: %d of tokens (%d inlined), %d of fonts; prelude %d",
			major, p.kit, len(tokens), len(compactCSS(string(tokens))), len(fonts), len(p.text))
		if p.kit > MaxKit {
			t.Errorf("kit v%d is %d bytes, over %d", major, p.kit, MaxKit)
		}
		document, err := Compose(major, strings.Repeat("a", MaxAuthorHTML))
		if err != nil {
			t.Errorf("the largest tile does not compose: %v", err)
		} else if len(document) > MaxDocument {
			t.Errorf("the largest document is %d bytes", len(document))
		}
	}
}

// The inlined text must not end its element early or switch the parser's
// script state.
func TestInlinedTextIsInert(t *testing.T) {
	style, script := kitParts(t, 1)
	for _, bad := range []string{"</script", "<script", "<!--"} {
		if strings.Contains(strings.ToLower(script), bad) {
			t.Errorf("the kit's script holds %q", bad)
		}
	}
	if strings.Contains(strings.ToLower(style), "</style") {
		t.Error("the kit's style holds </style")
	}
	if !strings.HasPrefix(script, "(function () {\n\"use strict\";\n") || !strings.HasSuffix(script, "\n})();") {
		t.Error("the scripts are not wrapped in one strict function")
	}
}

func TestSupported(t *testing.T) {
	if !Supported(1) || Supported(0) || Supported(2) {
		t.Error("Supported is wrong")
	}
	if !slices.Equal(Majors(), []int{1}) {
		t.Errorf("Majors = %v", Majors())
	}
}

func TestLint(t *testing.T) {
	script := func(body string) string { return "<script>\n" + body + "\n</script>" }
	cases := []struct {
		name string
		html string
		// rule is the finding wanted, or "" for none.
		rule     string
		severity string
		line     int
	}{
		{"clean", sampleTile, "", "", 0},
		{"external image", `<template><img src="https://example.invalid/x.png"></template>`, "external_url", Error, 1},
		{"protocol-relative", `<template><a href="//example.invalid">x</a></template>`, "external_url", Error, 1},
		{"css url", "<style>\n.x { background: url('https://example.invalid/x') }\n</style>", "external_url", Error, 2},
		{"script src assignment", script(`img.src = "https://example.invalid/p"`), "external_url", Error, 2},
		{"data url is fine", `<template><img src="data:image/png;base64,AAAA"></template>`, "", "", 0},
		{"dns-prefetch", `<link rel="dns-prefetch" href="//x.example.invalid">`, "resource_hint", Error, 1},
		{"preconnect", `<link rel=preconnect href="https://x.example.invalid">`, "resource_hint", Error, 1},
		{"stylesheet link", `<link rel="stylesheet" href="x.css">`, "forbidden_element", Error, 1},
		{"iframe", "\n<template><iframe></iframe></template>", "forbidden_element", Error, 2},
		{"object", `<template><object data="x"></object></template>`, "forbidden_element", Error, 1},
		{"embed", `<template><embed src="x"></template>`, "forbidden_element", Error, 1},
		{"form", `<template><form action="/x"></form></template>`, "forbidden_element", Error, 1},
		{"base", `<base href="/">`, "forbidden_element", Error, 1},
		{"meta refresh", `<meta http-equiv="refresh" content="0">`, "meta_http_equiv", Error, 1},
		{"meta csp", `<meta http-equiv="Content-Security-Policy" content="x">`, "meta_http_equiv", Error, 1},
		{"script src", `<script src="x.js"></script>`, "script_src", Error, 1},
		{"import", "<style>\n@import 'x.css';\n</style>", "css_import", Error, 2},
		{"fetch", script("fetch('/api')"), "network", Error, 2},
		{"xhr", script("new XMLHttpRequest()"), "network", Error, 2},
		{"websocket", script("new WebSocket('ws://x')"), "network", Error, 2},
		{"beacon", script("navigator.sendBeacon('/x')"), "network", Error, 2},
		{"rtc", script("new RTCPeerConnection()"), "network", Error, 2},
		{"worker", script("new Worker('x')"), "network", Error, 2},
		{"eval", script("eval('1')"), "eval", Error, 2},
		{"new Function", script("new Function('return 1')"), "eval", Error, 2},
		{"dynamic import", script("import('x')"), "eval", Error, 2},
		{"local storage", script("localStorage.getItem('x')"), "storage", Error, 2},
		{"cookie", script("document.cookie"), "storage", Error, 2},
		{"parent", script("parent.postMessage({}, '*')"), "parent", Error, 2},
		{"window.top", script("window.top.location"), "parent", Error, 2},
		{"rect.top is fine", script("const y = rect.top.toFixed(0);"), "", "", 0},
		{"location", script("location.href = '/x'"), "navigation", Error, 2},
		{"handler attribute", "<template><button onclick=\"x()\">x</button></template>", "event_handler", Error, 1},
		{"handler in baum.html", script("baum.html`<img onerror=${x}>`"), "event_handler", Error, 2},
		{"javascript url", `<template><a href="javascript:void 0">x</a></template>`, "javascript_url", Error, 1},
		{"unknown api", script("baum.render(x)"), "unknown_api", Error, 2},
		{"unknown refs api", script("baum.refs.all()"), "unknown_api", Error, 2},
		{"refs.get is fine", script("const p = baum.refs.get(c);"), "", "", 0},
		{"unknown class", `<template><p class="kit-headline">x</p></template>`, "unknown_class", Error, 1},
		{"vh", "<style>\n.x { min-height: 100vh; }\n</style>", "viewport_height", Warning, 2},
		{"dvh", "<style>\n.x { height: 50dvh; }\n</style>", "viewport_height", Warning, 2},
		{"prefers-color-scheme", "<style>@media (prefers-color-scheme: dark) {}</style>", "color_scheme", Warning, 1},
		{"unknown token", "<style>.x { color: var(--brand-forest); }</style>", "unknown_token", Warning, 1},
		{"own token is fine", "<style>.x { --gap: 4px; margin: var(--gap); }</style>", "", "", 0},
		{"innerHTML", script("root.innerHTML = x"), "inner_html", Warning, 2},
		{"static markup", "<style></style>\n<h1>Title</h1>", "static_markup", Warning, 2},
		{"window.open", script("window.open('x')"), "network", Error, 2},
		{"window.fetch", script("window.fetch('/api')"), "network", Error, 2},
		{"parent.postMessage", script("parent.postMessage({}, '*')"), "parent", Error, 2},
		{"window.location assignment", script("window.location = '/x'"), "navigation", Error, 2},
		{"document.location.href", script("document.location.href = '/x'"), "navigation", Error, 2},
		{"location.replace", script("location.replace('/x')"), "navigation", Error, 2},
		{"handler after other attributes", "<template><p class=\"a\" data-x=${y} onclick='x()'>x</p></template>", "event_handler", Error, 1},
		// Ordinary code that only looks like a mistake.
		{"a variable named top", script("const top = rows.slice(0, 3);\nreturn top.map((r) => r.name);"), "", "", 0},
		{"a variable named parent", script("const parent = node.parentNode;\nparent.append(x);"), "", "", 0},
		{"a javascript key", script(`const LANGS = { javascript: "JS", typescript: "TS" };`), "", "", 0},
		{"a loop with one", script("for (let i = 0; i<rows.length; i++) { const one = rows[i]; }"), "", "", 0},
		{"a variable named location", script("const location = item.path;"), "", "", 0},
		{"a member named location", script("item.location.href = x;"), "", "", 0},
		{"words in strings", script(`const KINDS = { ws: "WebSocket endpoint", sse: "EventSource stream", f: 'fetch (read)' };`), "", "", 0},
		{"words in a template", script("const t = `uses fetch( and localStorage`;"), "", "", 0},
		{"words in comments", script("// never fetch( here\n/* or eval( */\nconst x = 1;"), "", "", 0},
		{"a method named fetch", script("const rows = api.fetch(1);"), "", "", 0},
		{"a function named open", script("function open(x) { return x; }\nopen(1);"), "", "", 0},
		{"own document shell is fine", "<!doctype html><html><head><meta charset=utf-8><title>x</title></head><body>" + sampleTile + "</body></html>", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Lint(1, tc.html)
			if tc.rule == "" {
				if len(findings) > 0 {
					t.Errorf("Lint = %+v, want nothing", findings)
				}
				return
			}
			for _, finding := range findings {
				if finding.Rule == tc.rule {
					if finding.Severity != tc.severity || finding.Line != tc.line || finding.Message == "" {
						t.Errorf("finding = %+v, want %s on line %d", finding, tc.severity, tc.line)
					}
					return
				}
			}
			t.Errorf("Lint = %+v, want a %s finding", findings, tc.rule)
		})
	}
}

func TestLintUnsupportedMajor(t *testing.T) {
	findings := Lint(9, sampleTile)
	if len(findings) != 1 || findings[0].Rule != "kit" || findings[0].Severity != Error {
		t.Errorf("Lint = %+v", findings)
	}
}

func TestLintColumns(t *testing.T) {
	findings := Lint(1, "<style>\n  .é { height: 10vh }\n</style>")
	if len(findings) != 1 || findings[0].Line != 2 || findings[0].Column != 17 {
		t.Errorf("Lint = %+v, want line 2 column 17", findings)
	}
}

// The manifest is the contract: everything it names exists in the kit's
// sources and the kit offers nothing it does not name.
func TestManifestMatchesSources(t *testing.T) {
	manifest, err := LoadManifest(1)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if manifest.Kit != 1 {
		t.Errorf("manifest kit = %d", manifest.Kit)
	}

	// Every file is listed, and every listed file exists.
	listed := append(append([]string{"manifest.json"}, manifest.Runtime.Scripts...), manifest.Runtime.Styles...)
	entries, _ := fs.ReadDir(files, "v1")
	var present []string
	for _, entry := range entries {
		present = append(present, entry.Name())
	}
	slices.Sort(listed)
	if !slices.Equal(listed, present) {
		t.Errorf("v1 holds %v, the manifest lists %v", present, listed)
	}
	for _, name := range manifest.Runtime.Shared {
		read(name)
	}

	// API: the properties of window.baum, as bootstrap.js defines them.
	bootstrap := read("v1/bootstrap.js")
	object := between(t, bootstrap, `Object.defineProperty(window, "baum", {`, "\n});")
	defined := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^ {4}([a-zA-Z]+)\b`).FindAllStringSubmatch(object, -1) {
		defined["baum."+match[1]] = true
	}
	for _, match := range regexp.MustCompile(`(?m)^ {6}([a-zA-Z]+):`).FindAllStringSubmatch(object, -1) {
		defined["baum.refs."+match[1]] = true
	}
	delete(defined, "baum.value")
	delete(defined, "baum.refs")
	named := map[string]bool{}
	for _, entry := range manifest.API {
		named[entry.Name] = true
		if entry.Kind != "function" || entry.Signature == "" || entry.Description == "" {
			t.Errorf("api %s is not described", entry.Name)
		}
	}
	for name := range defined {
		if !named[name] {
			t.Errorf("window.baum defines %s, which the manifest lacks", name)
		}
	}
	for name := range named {
		if !defined[name] {
			t.Errorf("the manifest names %s, which window.baum lacks", name)
		}
	}

	// Elements: each is defined and made by its factory.
	elements := read("v1/elements.js")
	for _, entry := range manifest.Elements {
		factory := strings.TrimPrefix(entry.CreatedBy, "baum.")
		if !strings.Contains(bootstrap, fmt.Sprintf("defineElement(%q, %q)", entry.Name, factory)) {
			t.Errorf("%s is not defined for baum.%s", entry.Name, factory)
		}
		if !strings.Contains(elements+read("v1/markdown.js"), fmt.Sprintf("create(%q)", entry.Name)) {
			t.Errorf("no factory makes %s", entry.Name)
		}
	}

	// Classes: every kit-* selector of kit.css is listed, and every listed
	// class has a rule.
	css := read("v1/kit.css")
	selectors := map[string]bool{}
	for _, match := range regexp.MustCompile(`\.(kit-[a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		selectors[match[1]] = true
	}
	classes := map[string]bool{}
	for _, entry := range manifest.Classes {
		classes[entry.Name] = true
		if !selectors[entry.Name] {
			t.Errorf("class %s has no rule in kit.css", entry.Name)
		}
		for _, attribute := range entry.Attributes {
			for _, value := range attribute.Values {
				if !strings.Contains(css, fmt.Sprintf(`[%s="%s"]`, attribute.Name, value)) {
					t.Errorf("%s[%s=%q] has no rule in kit.css", entry.Name, attribute.Name, value)
				}
			}
		}
	}
	for name := range selectors {
		if !classes[name] {
			t.Errorf("kit.css styles .%s, which the manifest lacks", name)
		}
	}

	// Tokens: each is defined by the generated tokens or the kit.
	definitions := read("tokens.css") + css
	for _, entry := range manifest.Tokens {
		if !strings.Contains(definitions, entry.Name+":") {
			t.Errorf("token %s is not defined", entry.Name)
		}
		if entry.Group == "" || entry.Description == "" {
			t.Errorf("token %s is not described", entry.Name)
		}
	}

	// Names are unique.
	seen := map[string]bool{}
	for _, group := range [][]Entry{manifest.API, manifest.View, manifest.Elements, manifest.Classes, manifest.Tokens} {
		for _, entry := range group {
			if seen[entry.Kind+":"+entry.Name] {
				t.Errorf("%s %s is listed twice", entry.Kind, entry.Name)
			}
			seen[entry.Kind+":"+entry.Name] = true
		}
	}

	// The shipped bytes decode strictly into the documented shape.
	raw, _ := ManifestJSON(1)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&Manifest{}); err != nil {
		t.Errorf("manifest.json has fields Manifest lacks: %v", err)
	}
}

// The generated files are committed beside the package; the app's check
// regenerates and compares them. Here, only that they are there and marked.
func TestGeneratedFiles(t *testing.T) {
	for _, name := range []string{"tokens.css", "fonts.css"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "/* Generated by oss/app/scripts/build-tile-kit.mjs") {
			t.Errorf("%s is not marked as generated", name)
		}
	}
}

// TestComposeFiles writes the frame document of every .html file in
// $BAUM_KIT_COMPOSE_SRC to $BAUM_KIT_COMPOSE_OUT, failing on a lint error, for
// the app's opt-in browser pictures of the kit (`just
// local_app::tile-kit-screens`). Without them it is skipped.
func TestComposeFiles(t *testing.T) {
	src, out := os.Getenv("BAUM_KIT_COMPOSE_SRC"), os.Getenv("BAUM_KIT_COMPOSE_OUT")
	if src == "" || out == "" {
		t.Skip("set BAUM_KIT_COMPOSE_SRC and BAUM_KIT_COMPOSE_OUT to compose files")
	}
	paths, err := filepath.Glob(filepath.Join(src, "*.html"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no .html files in %s: %v", src, err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		html, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range Lint(1, string(html)) {
			t.Logf("%s:%d:%d: %s %s: %s", filepath.Base(path), finding.Line, finding.Column, finding.Severity, finding.Rule, finding.Message)
			if finding.Severity == Error && !strings.Contains(filepath.Base(path), "broken") {
				t.Errorf("%s has a lint error", filepath.Base(path))
			}
		}
		document, err := Compose(1, string(html))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, filepath.Base(path)), document, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompactJS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  // a comment\n  const a = 1;\n\n", "const a=1;"},
		{"/** A doc\n *  comment. */\nfoo( a, b );", "foo(a,b);"},
		{"/* one line */\nx;", "x;"},
		{"return value;", "return value;"},
		{"if (a === b) { c(); } else { d(); }", "if(a===b){c();}else{d();}"},
		{"a = b - -c + +d;", "a=b- -c+ +d;"},
		{`x = "a = b, c";`, `x="a = b, c";`},
		{`x = 'it\'s = ok' + y;`, `x='it\'s = ok'+y;`},
		{"x = `a ${b + 1} c` + d;", "x=`a ${b + 1} c`+d;"},
		// A template expression holding a literal keeps the rest of its line.
		{"x = `${f(\"a b\")} c`;  y = 1;", "x=`${f(\"a b\")} c`;  y = 1;"},
		// A line with a slash is kept whole: a regular expression stays exact.
		{"  if (/ a = b /.test(s)) { }", "if (/ a = b /.test(s)) { }"},
		{"const t = 0x85 /* mask */;", "const t = 0x85 /* mask */;"},
		// A character beyond ASCII counts as part of a name.
		{"a = é + b in c;", "a=é+b in c;"},
		{"x = é in c;", "x=é in c;"},
	}
	for _, tc := range cases {
		got, err := compactJS(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("compactJS(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"x = `a\nb`;", "/* a */ x;\n", "/* a\n*/ x;"} {
		if _, err := compactJS(bad); err == nil && bad != "/* a */ x;\n" {
			t.Errorf("compactJS(%q) did not fail", bad)
		}
	}
	// A one-line block comment followed by code keeps its line.
	if got, _ := compactJS("/* a */ x;"); got != "/* a */ x;" {
		t.Errorf("compactJS dropped code after a comment: %q", got)
	}
}

func TestCompactCSS(t *testing.T) {
	got := compactCSS("/* note */\n.a,\n.b {\n  color: var(--x);\n  margin: 0 auto;\n  font-family: \"A B\", serif;\n}\n\n.c {\n  box-shadow:\n    0 0 1px red,\n    0 0 2px blue;\n}\n")
	want := ".a,.b{color:var(--x);margin:0 auto;font-family:\"A B\",serif;}.c{box-shadow:\n0 0 1px red,0 0 2px blue;}"
	if got != want {
		t.Errorf("compactCSS = %q, want %q", got, want)
	}
}

// The kit's script as Compose inlines it, compacted. The app's unit tests
// run against this text as well as the sources (KIT_SCRIPT=inlined), which
// shows the compaction keeps what the code does.
func TestInlinedScriptGolden(t *testing.T) {
	p := preludeOf(1)
	if p.err != nil {
		t.Fatal(p.err)
	}
	goldentest.Text(t, "testdata/v1.inlined.js", p.script+"\n")
}

// v1 is frozen: every file of it is pinned by its digest.
func TestV1IsFrozen(t *testing.T) {
	manifest, err := LoadManifest(1)
	if err != nil {
		t.Fatal(err)
	}
	names := append(append([]string{"manifest.json"}, manifest.Runtime.Scripts...), manifest.Runtime.Styles...)
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		data, err := files.ReadFile("v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  v1/%s\n", hex.EncodeToString(sum[:]), name)
	}
	got := b.String()
	if os.Getenv("BAUM_KIT_REPIN") == "1" {
		if err := os.WriteFile("testdata/v1.sha256", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/v1.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("kit v1 changed. v1 is frozen: saved tiles are drawn with it for good, so it "+
			"changes only by addition (a new function, element, class or token), never by "+
			"removing, renaming or changing what a documented name does. If this change only "+
			"adds, update testdata/v1.sha256 in the same commit (BAUM_KIT_REPIN=1 go test "+
			"./tiles/kit -run TestV1IsFrozen) and say in the commit message what was added.\n"+
			"got:\n%s\nwant:\n%s", got, want)
	}
}

// The validator accepts a definition for kit majors 1 to tiles.KitLatest, and
// the frame route can only compose the majors this package ships.
func TestMajorsMatchWhatTheValidatorAccepts(t *testing.T) {
	for n := 0; n <= tiles.KitLatest+1; n++ {
		if Supported(n) != tiles.KitSupported(n) {
			t.Errorf("kit major %d: shipped %v, accepted by the validator %v", n, Supported(n), tiles.KitSupported(n))
		}
	}
}

// The guide names every name of the contract, so an author (or a model) who
// reads only the guide knows all of it; a name added to the manifest must be
// documented there too. The starter is written against the manifest alone.
func TestGuideNamesTheWholeContract(t *testing.T) {
	manifest, err := LoadManifest(guideKit)
	if err != nil {
		t.Fatal(err)
	}
	guide := Guide()
	for _, group := range [][]Entry{manifest.API, manifest.View, manifest.Elements, manifest.Classes, manifest.Tokens} {
		for _, entry := range group {
			if !strings.Contains(guide, "`"+entry.Name) && !strings.Contains(guide, entry.Name+"`") && !strings.Contains(guide, " "+entry.Name+" ") {
				t.Errorf("GUIDE.md does not name %s", entry.Name)
			}
		}
	}
	starter, err := fs.ReadFile(Starter(), "tile.html")
	if err != nil {
		t.Fatal(err)
	}
	if findings := Lint(guideKit, string(starter)); len(findings) > 0 {
		t.Errorf("the starter has lint findings: %+v", findings)
	}
}

// guideKit is the kit major the guide and the starter describe.
const guideKit = 1
