package kit

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Finding is one thing Lint found in a tile type's HTML.
type Finding struct {
	// Severity is "error" (the render check fails) or "warning".
	Severity string `json:"severity"`
	// Rule names what was found, such as "external_url" or "viewport_height".
	Rule    string `json:"rule"`
	Message string `json:"message"`
	// Line and Column are 1-based, in the author's HTML; Column counts
	// characters.
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Severities.
const (
	Error   = "error"
	Warning = "warning"
)

const maxFindings = 100

type lintRule struct {
	severity string
	rule     string
	pattern  *regexp.Regexp
	// in limits the rule to "script" or "style" contents; empty is the whole
	// HTML.
	in      string
	message func(match string) string
}

func fixed(text string) func(string) string { return func(string) string { return text } }

var lintRules = []lintRule{
	{Error, "external_url", regexp.MustCompile(`(?i)\b(?:src|href|poster|srcset|action|formaction|background|data)\s*=\s*["']?\s*(?:https?:|ftp:|wss?:|//)`), "",
		fixed("A tile can't load anything from the network: inline images as data: URLs and draw everything from its data.")},
	{Error, "external_url", regexp.MustCompile(`(?i)url\(\s*["']?\s*(?:https?:|ftp:|//)`), "",
		fixed("A tile can't load anything from the network: url() may only hold a data: URL.")},
	{Error, "external_url", regexp.MustCompile(`(?i)(?:\.(?:src|href)\s*=|setAttribute\(\s*["'](?:src|href)["']\s*,)\s*["'\x60]\s*(?:https?:|ftp:|//)`), "script",
		fixed("A tile can't load anything from the network.")},
	{Error, "resource_hint", regexp.MustCompile(`(?i)<link\b[^>]*\brel\s*=\s*["']?\s*(?:dns-prefetch|preconnect|prefetch|preload|prerender|modulepreload)`), "",
		fixed("Resource hints such as dns-prefetch and preconnect reach the network; a tile has nothing to fetch.")},
	{Error, "forbidden_element", regexp.MustCompile(`(?i)<(?:link|base|iframe|frame|frameset|object|embed|form|portal)\b`), "",
		func(match string) string {
			return fmt.Sprintf("%s> isn't allowed in a tile: no links to other documents, nested frames, plugins, forms or base URLs.", strings.ToLower(match))
		}},
	{Error, "meta_http_equiv", regexp.MustCompile(`(?i)<meta\b[^>]*\bhttp-equiv\b`), "",
		fixed("<meta http-equiv> isn't allowed: a refresh or policy in a tile does nothing but break it.")},
	{Error, "script_src", regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=`), "",
		fixed("A script must be inline: <script src> can't load in a tile.")},
	{Error, "css_import", regexp.MustCompile(`(?i)@import\b`), "style",
		fixed("@import can't load in a tile; put the styles in the tile's own <style>.")},
	{Error, "event_handler", regexp.MustCompile(`(?i)<[a-z][^<>]*?\son[a-z]+\s*=`), "",
		fixed("Inline event handler attributes are refused by baum.html; add listeners with addEventListener.")},
	{Error, "javascript_url", regexp.MustCompile(`(?i)\bjavascript\s*:`), "",
		fixed("javascript: URLs are refused; add listeners with addEventListener.")},
	{Error, "network", regexp.MustCompile(`\b(?:fetch\s*\(|XMLHttpRequest\b|WebSocket\b|EventSource\b|sendBeacon\b|RTCPeerConnection\b|new\s+(?:Shared)?Worker\b|importScripts\b|window\.open\s*\()`), "script",
		func(match string) string {
			return fmt.Sprintf("%s: a tile has no network, workers or windows; everything it shows comes in its data.", strings.TrimSpace(strings.TrimRight(match, "( ")))
		}},
	{Error, "eval", regexp.MustCompile(`\b(?:eval\s*\(|new\s+Function\b|import\s*\()`), "script",
		fixed("eval, new Function and import() are blocked in a tile.")},
	{Error, "storage", regexp.MustCompile(`\b(?:localStorage|sessionStorage|indexedDB|document\.cookie|caches\s*\.)`), "script",
		fixed("A tile has no storage or cookies: it draws only from its data.")},
	{Error, "navigation", regexp.MustCompile(`\b(?:location\s*(?:\.\s*(?:href|assign|replace|reload)\b|=[^=])|history\s*\.\s*(?:pushState|replaceState|back|go)\b)`), "script",
		fixed("A tile can't navigate; use baum.jump(citation) to take the reader to cited code.")},
	{Warning, "viewport_height", regexp.MustCompile(`(?i)\d(?:[dsl]?vh|[dsl]?vb|[dsl]?vmin|[dsl]?vmax)\b`), "",
		fixed("Viewport units follow the frame's height, which follows the tile's: a detail grows without end. Size by content instead.")},
	{Warning, "color_scheme", regexp.MustCompile(`(?i)prefers-color-scheme`), "",
		fixed("prefers-color-scheme follows the system, not the app's theme; colour with the kit's tokens, which follow the app.")},
	{Warning, "inner_html", regexp.MustCompile(`\b(?:innerHTML|outerHTML|insertAdjacentHTML|document\.write(?:ln)?)\b`), "script",
		fixed("Build markup with baum.html, which escapes data; innerHTML parses it.")},
}

var (
	blockPatterns = map[string]*regexp.Regexp{
		"script":   regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)(?:</script\s*>|\z)`),
		"style":    regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)(?:</style\s*>|\z)`),
		"template": regexp.MustCompile(`(?is)<template\b[^>]*>(.*?)(?:</template\s*>|\z)`),
	}
	commentPattern = regexp.MustCompile(`(?s)<!--.*?(?:-->|\z)`)
	// Tags that only frame a document, which the parser drops in the body.
	shellPattern  = regexp.MustCompile(`(?is)<title\b[^>]*>.*?</title\s*>|</?(?:!doctype|html|head|body)\b[^>]*>|<meta\b[^>]*>`)
	tagPattern    = regexp.MustCompile(`<[a-zA-Z][^>]*>|\S`)
	baumPattern   = regexp.MustCompile(`\bbaum\s*\.\s*([A-Za-z_$][\w$]*)(?:\s*\.\s*([A-Za-z_$][\w$]*))?`)
	classPattern  = regexp.MustCompile(`kit-[a-z0-9-]+`)
	varPattern    = regexp.MustCompile(`var\(\s*(--[\w-]+)`)
	definePattern = regexp.MustCompile(`(--[\w-]+)\s*:|setProperty\(\s*["'](--[\w-]+)`)
	parentPattern = regexp.MustCompile(`\b(?:window\s*\.\s*)?(?:parent|top|opener)\s*\.\s*[A-Za-z_$]`)
)

type span struct{ start, end int }

// Lint reports mistakes in a tile type's HTML written for kit major n:
// external URLs and resource hints, elements a tile can't use (<link>,
// <base>, <iframe>, <object>, <embed>, <form>, <meta http-equiv>,
// <script src>), network, storage, eval, navigation and parent access in
// scripts, inline event handlers and javascript: URLs, and names the
// manifest doesn't have (a baum.* API or a kit-* class); and, as warnings,
// viewport-height units, prefers-color-scheme, tokens the manifest and the
// tile don't define, innerHTML, and static markup outside <script>, <style>
// and <template>.
//
// It is a scanner over the text, for honest mistakes at creation, and not a
// security boundary: the frame's sandbox and CSP are. It can be fooled and
// can misread a comment; findings are advice the render check reports.
func Lint(n int, html string) []Finding {
	manifest, err := LoadManifest(n)
	if err != nil {
		return []Finding{{Severity: Error, Rule: "kit", Message: fmt.Sprintf("This build has no kit %d.", n), Line: 1, Column: 1}}
	}
	l := linter{html: html}
	l.index()
	for _, kind := range []string{"script", "style", "template"} {
		for _, match := range blockPatterns[kind].FindAllStringSubmatchIndex(html, -1) {
			l.blocks = append(l.blocks, span{match[0], match[1]})
			switch kind {
			case "script":
				l.scripts = append(l.scripts, span{match[2], match[3]})
			case "style":
				l.styles = append(l.styles, span{match[2], match[3]})
			}
		}
	}
	for _, rule := range lintRules {
		for _, s := range l.spans(rule.in) {
			for _, match := range rule.pattern.FindAllStringIndex(html[s.start:s.end], -1) {
				at := s.start + match[0]
				l.add(rule.severity, rule.rule, rule.message(html[at:s.start+match[1]]), at)
			}
		}
	}
	l.parentAccess()
	l.names(manifest)
	l.staticMarkup()
	sort.SliceStable(l.findings, func(i, j int) bool {
		a, b := l.findings[i], l.findings[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	if len(l.findings) > maxFindings {
		l.findings = l.findings[:maxFindings]
	}
	return l.findings
}

type linter struct {
	html     string
	lines    []int
	blocks   []span
	scripts  []span
	styles   []span
	findings []Finding
	seen     map[string]bool
}

func (l *linter) index() {
	l.lines = []int{0}
	for i := 0; i < len(l.html); i++ {
		if l.html[i] == '\n' {
			l.lines = append(l.lines, i+1)
		}
	}
	l.seen = map[string]bool{}
}

func (l *linter) spans(in string) []span {
	switch in {
	case "script":
		return l.scripts
	case "style":
		return l.styles
	}
	return []span{{0, len(l.html)}}
}

func (l *linter) add(severity, rule, message string, offset int) {
	line := sort.Search(len(l.lines), func(i int) bool { return l.lines[i] > offset })
	column := utf8.RuneCountInString(l.html[l.lines[line-1]:offset]) + 1
	// One finding of a rule per line is enough to find it.
	key := fmt.Sprintf("%s:%d", rule, line)
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.findings = append(l.findings, Finding{Severity: severity, Rule: rule, Message: message, Line: line, Column: column})
}

// identifierBefore reports whether the byte before offset continues a name
// or a member access, so `foo.parent.x` and `xparent.x` are not parent
// access.
func (l *linter) identifierBefore(offset int, extra string) bool {
	if offset == 0 {
		return false
	}
	c := l.html[offset-1]
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte(extra, c) >= 0
}

func (l *linter) parentAccess() {
	for _, s := range l.scripts {
		for _, match := range parentPattern.FindAllStringIndex(l.html[s.start:s.end], -1) {
			at := s.start + match[0]
			if l.identifierBefore(at, ".") {
				continue
			}
			l.add(Error, "parent", "A tile can't reach the page around it; talk to it only through baum.jump, baum.ask and baum.openDetail.", at)
		}
	}
}

func (l *linter) names(manifest *Manifest) {
	api := map[string]bool{}
	prefixes := map[string]bool{}
	for _, entry := range manifest.API {
		api[entry.Name] = true
		if parts := strings.Split(entry.Name, "."); len(parts) > 2 {
			prefixes[strings.Join(parts[:2], ".")] = true
		}
	}
	for _, s := range l.scripts {
		for _, match := range baumPattern.FindAllStringSubmatchIndex(l.html[s.start:s.end], -1) {
			at := s.start + match[0]
			if l.identifierBefore(at, ".") {
				continue
			}
			first := "baum." + l.html[s.start+match[2]:s.start+match[3]]
			name := first
			if prefixes[first] && match[4] >= 0 {
				name = first + "." + l.html[s.start+match[4]:s.start+match[5]]
			}
			if !api[name] && !(prefixes[first] && match[4] < 0) {
				l.add(Error, "unknown_api", fmt.Sprintf("%s isn't part of kit %d.", name, manifest.Kit), at)
			}
		}
	}
	classes := map[string]bool{}
	for _, entry := range manifest.Classes {
		classes[entry.Name] = true
	}
	for _, match := range classPattern.FindAllStringIndex(l.html, -1) {
		if l.identifierBefore(match[0], "-") {
			continue
		}
		if name := l.html[match[0]:match[1]]; !classes[name] {
			l.add(Error, "unknown_class", fmt.Sprintf("%s isn't a kit %d class; kit- names are the kit's.", name, manifest.Kit), match[0])
		}
	}
	tokens := map[string]bool{}
	for _, entry := range manifest.Tokens {
		tokens[entry.Name] = true
	}
	for _, match := range definePattern.FindAllStringSubmatch(l.html, -1) {
		tokens[match[1]+match[2]] = true
	}
	for _, match := range varPattern.FindAllStringSubmatchIndex(l.html, -1) {
		if name := l.html[match[2]:match[3]]; !tokens[name] {
			l.add(Warning, "unknown_token", fmt.Sprintf("%s isn't a kit %d token; it may change or disappear.", name, manifest.Kit), match[0])
		}
	}
}

// staticMarkup warns about content outside <script>, <style> and
// <template>: the render functions own what a tile shows.
func (l *linter) staticMarkup() {
	rest := []byte(l.html)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if rest[i] != '\n' {
				rest[i] = ' '
			}
		}
	}
	for _, s := range l.blocks {
		blank(s.start, s.end)
	}
	for _, pattern := range []*regexp.Regexp{commentPattern, shellPattern} {
		for _, match := range pattern.FindAllIndex(rest, -1) {
			blank(match[0], match[1])
		}
	}
	if match := tagPattern.FindIndex(rest); match != nil {
		l.add(Warning, "static_markup", "Draw what the tile shows from its render functions; markup outside <script>, <style> and <template> shows before the data arrives.", match[0])
	}
}
