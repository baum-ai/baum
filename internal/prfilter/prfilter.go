// Package prfilter reads and applies the pull-request filter language, such
// as `repo=acme/app is!=draft (review=approved OR risk>=4) flaky`. It is the
// Go twin of app/src/pr-filter.ts: the two must agree, and the shared
// testdata/cases.json keeps them so.
package prfilter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/reviewindex"
)

// MaxQuery is the longest query, in UTF-16 units as JavaScript counts them.
const MaxQuery = 500

// Expr kinds.
const (
	KindAnd  = "and"
	KindOr   = "or"
	KindNot  = "not"
	KindTerm = "term"
	KindText = "text"
)

// Node is a parsed filter expression.
type Node struct {
	Kind string
	// Items are the operands of an and/or; Item is the operand of a not.
	Items []Expr
	Item  Expr
	// A term's field, operator and values.
	Key    string
	Op     string
	Values []string
	// Text is the word a text expression looks for.
	Text string
	// Grouped is set on an expression written in parentheses of its own.
	Grouped bool
}

// Expr is a parsed filter; nil is the empty filter.
type Expr = *Node

// isSpace is JavaScript's /\s/, which differs from unicode.IsSpace (U+FEFF
// is in, U+0085 is out).
func isSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trim(s string) string { return strings.TrimFunc(s, isSpace) }

func lower(s string) string { return strings.ToLower(s) }

func same(a, b string) bool { return lower(a) == lower(b) }

// ownerOf is the owner of owner/name, or of host/owner/name.
func ownerOf(repository string) string {
	parts := strings.Split(repository, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}

type field struct {
	key string
	// ops is nil for = and !=.
	ops   []string
	check func(value, op string) string
	test  func(value string, e reviewindex.Entry, op, kind string) bool
}

type row struct {
	value string
	test  func(e reviewindex.Entry) bool
}

var (
	equality    = []string{"=", "!="}
	comparisons = []string{"=", "!=", ">=", "<=", ">", "<"}

	isTable = []row{
		{"draft", func(e reviewindex.Entry) bool { return e.PullRequest.Draft }},
		{"mergeable", func(e reviewindex.Entry) bool { return e.Merge == "ready" }},
		{"conflict", func(e reviewindex.Entry) bool { return e.Merge == "conflict" }},
		{"blocked", func(e reviewindex.Entry) bool { return e.Merge == "blocked" }},
		{"behind", func(e reviewindex.Entry) bool { return e.PullRequest.Behind }},
		{"bot", func(e reviewindex.Entry) bool { return e.PullRequest.AuthorBot }},
	}
	reviewTable = []row{
		{"approved", func(e reviewindex.Entry) bool { return e.Review == "approved" }},
		{"changes_requested", func(e reviewindex.Entry) bool { return e.Review == "changes_requested" }},
		{"none", func(e reviewindex.Entry) bool { return e.Review != "approved" && e.Review != "changes_requested" }},
	}
	checksTable = []row{
		{"passing", func(e reviewindex.Entry) bool { return e.Checks == "passing" }},
		{"failing", func(e reviewindex.Entry) bool { return e.Checks == "failing" }},
		{"running", func(e reviewindex.Entry) bool { return e.Checks == "running" }},
		{"none", func(e reviewindex.Entry) bool { return e.Checks == "none" || e.Checks == "" }},
	}
	waitingTable = []struct {
		value string
		test  func(kind string) bool
	}{
		{"review", func(k string) bool { return k == "review" || k == "clone" }},
		{"merge", func(k string) bool { return k == "merge" }},
		{"agent", func(k string) bool { return k == "fix" || k == "update" }},
		{"other", func(k string) bool { return k == "other" }},
	}
)

// listed is a field that takes only the given values.
func listed(key string, values []string, test func(value string, e reviewindex.Entry, op, kind string) bool) *field {
	return &field{
		key: key,
		check: func(value, _ string) string {
			for _, one := range values {
				if same(one, value) {
					return ""
				}
			}
			return key + " takes " + strings.Join(values, ", ") + "."
		},
		test: test,
	}
}

func fromTable(key string, table []row) *field {
	values := make([]string, len(table))
	for i, r := range table {
		values[i] = r.value
	}
	return listed(key, values, func(value string, e reviewindex.Entry, _, _ string) bool {
		v := lower(value)
		for _, r := range table {
			if r.value == v {
				return r.test(e)
			}
		}
		return false
	})
}

// me is a field of who a pull request is for: @me, or none for nobody.
func me(key string, mine, anyone func(e reviewindex.Entry) bool) *field {
	return listed(key, []string{"@me", "none"}, func(value string, e reviewindex.Entry, _, _ string) bool {
		if same(value, "none") {
			return !anyone(e) && !mine(e)
		}
		return mine(e)
	})
}

// score is the entry's risk score, if it has one.
func score(e reviewindex.Entry) (float64, bool) {
	if len(e.RiskScore) == 0 {
		return 0, false
	}
	var r struct {
		Score *float64 `json:"score"`
	}
	if json.Unmarshal(e.RiskScore, &r) != nil || r.Score == nil {
		return 0, false
	}
	return *r.Score, true
}

func riskTest(value string, e reviewindex.Entry, op, _ string) bool {
	s, ok := score(e)
	if same(value, "none") {
		return !ok
	}
	if len(value) != 1 || value[0] < '1' || value[0] > '5' || !ok {
		return false
	}
	n := float64(value[0] - '0')
	switch op {
	case ">=":
		return s >= n
	case "<=":
		return s <= n
	case ">":
		return s > n
	case "<":
		return s < n
	}
	return s == n
}

func contains(list []string, v string) bool {
	for _, one := range list {
		if one == v {
			return true
		}
	}
	return false
}

var fields = []*field{
	{
		key: "repo",
		test: func(value string, e reviewindex.Entry, _, _ string) bool {
			repository := e.PullRequest.Repository
			if same(repository, value) {
				return true
			}
			parts := strings.Split(repository, "/")
			return same(parts[len(parts)-1], value)
		},
	},
	{
		key: "org",
		test: func(value string, e reviewindex.Entry, _, _ string) bool {
			return same(ownerOf(e.PullRequest.Repository), value)
		},
	},
	{
		key: "author",
		test: func(value string, e reviewindex.Entry, _, _ string) bool {
			if same(value, "@me") {
				return e.PullRequest.AuthoredByMe
			}
			return same(e.PullRequest.Author, value)
		},
	},
	me("assignee",
		func(e reviewindex.Entry) bool { return e.PullRequest.AssignedToMe },
		func(e reviewindex.Entry) bool { return e.PullRequest.Assigned }),
	me("review-requested",
		func(e reviewindex.Entry) bool { return e.PullRequest.ReviewRequestedFromMe },
		func(e reviewindex.Entry) bool { return e.PullRequest.ReviewRequested }),
	fromTable("review", reviewTable),
	fromTable("checks", checksTable),
	fromTable("is", isTable),
	listed("waiting", []string{"review", "merge", "agent", "other"},
		func(value string, _ reviewindex.Entry, _, kind string) bool {
			if kind == "" {
				return false
			}
			v := lower(value)
			for _, w := range waitingTable {
				if w.value == v {
					return w.test(kind)
				}
			}
			return false
		}),
	{
		key: "risk",
		ops: comparisons,
		check: func(value, op string) string {
			isScore := len(value) == 1 && value[0] >= '1' && value[0] <= '5'
			switch {
			case isScore || (same(value, "none") && contains(equality, op)):
				return ""
			case same(value, "none"):
				return "risk compares only scores: none takes = or !=."
			}
			return "risk takes 1 to 5, or none."
		},
		test: riskTest,
	},
	{
		key: "base",
		test: func(value string, e reviewindex.Entry, _, _ string) bool {
			return same(e.PullRequest.BaseRef, value)
		},
	},
}

func fieldOf(key string) *field {
	k := lower(key)
	for _, f := range fields {
		if f.key == k {
			return f
		}
	}
	return nil
}

type token struct {
	kind       string // "(", ")", "AND", "OR", "-(", "word"
	text       string
	start, end int
}

// tokenize splits the query into parentheses, operators and words; a word
// keeps any "quoted" part whole. An unclosed quote runs to the end.
func tokenize(query string) []token {
	var out []token
	i := 0
	at := func(j int) (rune, int) { return utf8.DecodeRuneInString(query[j:]) }
	for i < len(query) {
		c, w := at(i)
		switch {
		case isSpace(c):
			i += w
		case c == '(' || c == ')':
			out = append(out, token{kind: string(c), start: i, end: i + 1})
			i++
		case c == '-' && i+1 < len(query) && query[i+1] == '(':
			out = append(out, token{kind: "-(", start: i, end: i + 1})
			i++
		default:
			start := i
			var text strings.Builder
			for i < len(query) {
				r, rw := at(i)
				if isSpace(r) || r == '(' || r == ')' {
					break
				}
				if r == '"' {
					close := strings.IndexByte(query[i+1:], '"')
					if close < 0 {
						text.WriteString(query[i+1:])
						i = len(query)
					} else {
						text.WriteString(query[i+1 : i+1+close])
						i += close + 2
					}
				} else {
					text.WriteString(query[i : i+rw])
					i += rw
				}
			}
			raw := query[start:i]
			if raw == "AND" || raw == "OR" {
				out = append(out, token{kind: raw, start: start, end: i})
			} else {
				out = append(out, token{kind: "word", text: text.String(), start: start, end: i})
			}
		}
	}
	return out
}

var (
	termRE     = regexp.MustCompile(`(?s)^([A-Za-z][A-Za-z-]*)(!=|>=|<=|=|:|>|<)(.*)$`)
	comparedRE = regexp.MustCompile(`^(>=|<=|>|<)`)
)

type termParts struct {
	key, op string
	values  []string
}

// splitTerm is a word's key, operator and values when it is a term.
func splitTerm(query string, t token) *termParts {
	raw := strings.TrimPrefix(query[t.start:t.end], "-")
	m := termRE.FindStringSubmatch(raw)
	if m == nil {
		return nil
	}
	op := m[2]
	if op == ":" {
		op = "="
	}
	rest := m[3]
	if m[2] == ":" {
		if c := comparedRE.FindString(rest); c != "" {
			op = c
			rest = rest[len(c):]
		}
	}
	var values []string
	var current strings.Builder
	inQuote := false
	for _, c := range rest {
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			values = append(values, current.String())
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	values = append(values, current.String())
	return &termParts{key: lower(m[1]), op: op, values: values}
}

type syntaxError string

func fail(format string, args ...any) { panic(syntaxError(fmt.Sprintf(format, args...))) }

// parse reads the query; nil for an empty one. Failures panic with a
// syntaxError.
func parse(query string) Expr {
	tokens := tokenize(query)
	at := 0
	var or func() Expr
	var operand func() Expr
	operand = func() Expr {
		if at >= len(tokens) {
			fail("The query ends where a filter should follow.")
		}
		t := tokens[at]
		at++
		if t.kind == "-(" {
			return &Node{Kind: KindNot, Item: operand()}
		}
		if t.kind == "(" {
			inner := or()
			if at >= len(tokens) || tokens[at].kind != ")" {
				fail("A parenthesis is not closed.")
			}
			at++
			inner.Grouped = true
			return inner
		}
		if t.kind != "word" {
			fail("%s needs a filter before it.", t.kind)
		}
		negated := query[t.start] == '-' && t.end-t.start > 1
		var expr Expr
		if term := splitTerm(query, t); term != nil {
			f := fieldOf(term.key)
			if f == nil {
				keys := make([]string, len(fields))
				for i, one := range fields {
					keys[i] = one.key
				}
				fail("Unknown filter \"%s\". Try %s.", term.key, strings.Join(keys, ", "))
			}
			ops := f.ops
			if ops == nil {
				ops = equality
			}
			if !contains(ops, term.op) {
				fail("%s takes %s.", f.key, strings.Join(ops, " or "))
			}
			for _, v := range term.values {
				if trim(v) == "" {
					fail("%s%s needs a value.", f.key, term.op)
				}
			}
			if f.check != nil {
				for _, v := range term.values {
					if problem := f.check(v, term.op); problem != "" {
						fail("%s", problem)
					}
				}
			}
			expr = &Node{Kind: KindTerm, Key: f.key, Op: term.op, Values: term.values}
		} else {
			text := t.text
			if negated {
				// The word's text starts with the "-" itself.
				_, w := utf8.DecodeRuneInString(text)
				text = text[w:]
			}
			if text == "" {
				fail("A - needs a filter after it.")
			}
			expr = &Node{Kind: KindText, Text: text}
		}
		if negated {
			return &Node{Kind: KindNot, Item: expr}
		}
		return expr
	}
	and := func() Expr {
		items := []Expr{operand()}
		for at < len(tokens) && tokens[at].kind != "OR" && tokens[at].kind != ")" {
			if tokens[at].kind == "AND" {
				at++
			}
			items = append(items, operand())
		}
		if len(items) == 1 {
			return items[0]
		}
		return &Node{Kind: KindAnd, Items: items}
	}
	or = func() Expr {
		items := []Expr{and()}
		for at < len(tokens) && tokens[at].kind == "OR" {
			at++
			items = append(items, and())
		}
		if len(items) == 1 {
			return items[0]
		}
		return &Node{Kind: KindOr, Items: items}
	}
	if len(tokens) == 0 {
		return nil
	}
	expr := or()
	if at < len(tokens) {
		fail("A closing parenthesis has no opening one.")
	}
	return expr
}

type parsed struct {
	expr Expr
	err  error
}

func parseSafe(query string) (p parsed) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(syntaxError)
			if !ok {
				panic(r)
			}
			p = parsed{err: fmt.Errorf("%s", string(e))}
		}
	}()
	return parsed{expr: parse(query)}
}

var cache = struct {
	sync.Mutex
	m map[string]parsed
}{m: map[string]parsed{}}

func parseCached(query string) parsed {
	cache.Lock()
	p, ok := cache.m[query]
	cache.Unlock()
	if ok {
		return p
	}
	p = parseSafe(query)
	cache.Lock()
	if len(cache.m) > 200 {
		clear(cache.m)
	}
	cache.m[query] = p
	cache.Unlock()
	return p
}

// Parse reads a query. A nil Expr and nil error mean an empty query; an
// error carries the same message as the TypeScript parser's. The result is
// shared and must not be changed.
func Parse(query string) (Expr, error) {
	p := parseCached(query)
	return p.expr, p.err
}

func (x *Node) test(e reviewindex.Entry, kind string) bool {
	switch x.Kind {
	case KindAnd:
		for _, one := range x.Items {
			if !one.test(e, kind) {
				return false
			}
		}
		return true
	case KindOr:
		for _, one := range x.Items {
			if one.test(e, kind) {
				return true
			}
		}
		return false
	case KindNot:
		return !x.Item.test(e, kind)
	case KindTerm:
		f := fieldOf(x.Key)
		if f == nil {
			return false
		}
		op := x.Op
		if op == "!=" {
			op = "="
		}
		found := false
		for _, v := range x.Values {
			if f.test(v, e, op, kind) {
				found = true
				break
			}
		}
		if x.Op == "!=" {
			return !found
		}
		return found
	case KindText:
		pr := e.PullRequest
		text := lower(x.Text)
		number := strconv.Itoa(pr.Number)
		return strings.Contains(lower(pr.Title), text) ||
			strings.Contains(lower(pr.Repository), text) ||
			"#"+number == text || number == text
	}
	return false
}

// utf16Len is the length JavaScript gives the string.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Problem says why the query cannot be saved: it does not read, or is longer
// than the daemon keeps. "" when it can.
func Problem(query string) string {
	if _, err := Parse(query); err != nil {
		return err.Error()
	}
	if utf16Len(query) > MaxQuery {
		return fmt.Sprintf("A filter is at most %d characters.", MaxQuery)
	}
	return ""
}

// Matcher reports whether a pull request passes a compiled query; kind is
// what it waits for in an inbox's queue ("" for none, where waiting holds
// for nothing).
type Matcher func(e reviewindex.Entry, kind string) bool

// Compile parses the query once. A query that cannot be read, or an empty
// one, narrows nothing.
func Compile(query string) Matcher {
	expr, err := Parse(query)
	if err != nil || expr == nil {
		return func(reviewindex.Entry, string) bool { return true }
	}
	return expr.test
}

// Matches reports whether the pull request passes the query.
func Matches(query string, e reviewindex.Entry, kind string) bool {
	return Compile(query)(e, kind)
}

// quote writes a value as a query reads it back.
func quote(value string) string {
	ok := value != ""
	for _, r := range value {
		if isSpace(r) || r == ',' || r == '(' || r == ')' || r == '"' {
			ok = false
			break
		}
	}
	if ok {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, "") + `"`
}

func quoteAll(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = quote(v)
	}
	return strings.Join(out, ",")
}

// tidy puts the query on one line: control characters and runs of space
// become one space.
func tidy(query string) string {
	var b strings.Builder
	gap := false
	for _, r := range query {
		if unicode.IsControl(r) || isSpace(r) {
			if !gap {
				b.WriteByte(' ')
			}
			gap = true
			continue
		}
		gap = false
		b.WriteRune(r)
	}
	return trim(b.String())
}

func andQuery(first, second string) string {
	wrap := func(query string) string {
		expr, err := Parse(query)
		if err == nil && expr != nil && expr.Kind == KindOr && !expr.Grouped {
			return "(" + trim(query) + ")"
		}
		return trim(query)
	}
	var parts []string
	for _, one := range []string{first, second} {
		if trim(one) != "" {
			parts = append(parts, one)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return tidy(parts[0])
	}
	return tidy(wrap(parts[0]) + " " + wrap(parts[1]))
}

// Parts are the fields of an inbox that FoldedQuery folds into one query.
type Parts struct {
	What         string
	Owners       []string
	Risk         string
	Repositories []string
	Query        string
	NoBots       bool
}

// FoldedQuery is an inbox's parts as one query: what it holds (waiting= or,
// for the person's own, author=@me), owners (org=), risk (risk>=),
// repositories (repo=) and bots (is!=bot), then its own query.
func FoldedQuery(p Parts) string {
	var parts []string
	switch {
	case p.What == "mine":
		parts = append(parts, "author=@me")
	case p.What != "any":
		parts = append(parts, "waiting="+p.What)
	}
	if len(p.Owners) > 0 {
		parts = append(parts, "org="+quoteAll(p.Owners))
	}
	if p.Risk != "any" {
		parts = append(parts, "risk>="+p.Risk)
	}
	if len(p.Repositories) > 0 {
		parts = append(parts, "repo="+quoteAll(p.Repositories))
	}
	if p.NoBots {
		parts = append(parts, "is!=bot")
	}
	own := p.Query
	if _, err := Parse(own); err != nil {
		// A query that does not read narrows nothing, and must not undo the parts.
		own = ""
	}
	return andQuery(strings.Join(parts, " "), own)
}
