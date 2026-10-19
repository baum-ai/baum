// Package redact removes secrets and e-mail addresses from text before it is
// shown, stored or sent to a model provider.
//
// Everything goes through one goredact engine: the balanced built-in rule
// set plus a custom rule for e-mail addresses. Spans are masked in place with
// a format-preserving mask, so a masked value keeps its length and shape and
// the text around it stays valid. Newlines a mask would overwrite are put
// back, so line numbers in a patch survive a secret that spans lines.
package redact

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/goredact"
)

// EmailRuleID is the id the e-mail rule reports its findings under.
const EmailRuleID = "email-address"

// Config is how every byte baum redacts is redacted. RecordAligned keeps
// emitted output on newline boundaries so JSONL streams stay whole records.
var Config = goredact.Config{
	Profile:       goredact.ProfileBalanced,
	MaskStrategy:  goredact.MaskFormatPreserving,
	RecordAligned: true,
	CustomRules:   []goredact.CustomRule{emailRule},
}

// The e-mail rule's window: up to 254 bytes, the longest address there can
// be, on each side of the @.
const (
	emailLookbehind = 254
	emailLookahead  = 254
)

// emailRule masks e-mail addresses. Every @, and its percent-encoded form
// %40, triggers it; the validator then looks for a local part before the
// trigger and a dotted domain after it, never crossing a newline. Letters
// may be any Unicode letter, so internationalised addresses are masked too.
var emailRule = goredact.CustomRule{
	ID:            EmailRuleID,
	Triggers:      []string{"@", "%40"},
	Confidence:    goredact.ConfidenceMedium,
	MaxLookbehind: emailLookbehind,
	MaxLookahead:  emailLookahead,
	Validate:      validateEmail,
}

func validateEmail(window []byte, trigStart, trigEnd int) (start, end int, ok bool) {
	return matchEmail(window, trigStart, trigEnd, trigEnd+emailLookahead == len(window), nil)
}

// localScan remembers where the previous trigger's local part began, so
// that a run of local-part bytes holding many triggers ("a%40a%40a%40...")
// is scanned once, not once per trigger.
type localScan struct {
	at, start int
	valid     bool
}

// matchEmail finds the address around the @ at text[at:after]. It masks at
// least what the pattern
//
//	(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b
//
// matches: the whole run of local-part bytes before the @, and the domain
// up to the furthest label of two or more letters that ends on a word
// boundary, so a trailing ".1", "-1" or "2" leaves the address itself
// masked. Any Unicode letter counts as a letter, as do "K" (U+212A) and "ſ" (U+017F)
// under (?i).
// When the domain runs to the end of a window cut short (cut), the
// address may go on past it, so the whole visible run is masked, provided
// it already holds a dot: a dotless run is no address, and an exact-fit
// window must not turn it into one.
//
// prev, when not nil, carries the scan of the previous trigger in the same
// text, which must lie before at; it keeps a scan over many triggers linear.
func matchEmail(text []byte, at, after int, cut bool, prev *localScan) (start, end int, ok bool) {
	start = at
	for start > 0 {
		if prev != nil && prev.valid && start == prev.at {
			// The run reached the previous trigger, which is local-part
			// bytes itself; it continues back exactly as that scan found.
			start = prev.start
			break
		}
		if isLocalPartByte(text[start-1]) {
			start--
		} else if n := unicodeLetterBefore(text, start); n > 0 {
			start -= n
		} else {
			break
		}
	}
	if prev != nil {
		*prev = localScan{at: at, start: start, valid: true}
	}
	if start == at {
		return 0, 0, false
	}
	run := after
	for run < len(text) {
		if isDomainByte(text[run]) {
			run++
		} else if n := unicodeLetterAt(text, run); n > 0 {
			run += n
		} else {
			break
		}
	}
	if cut && run == len(text) && after+1 < run && bytes.IndexByte(text[after+1:run], '.') >= 0 {
		return start, run, true
	}
	end = -1
	// The domain needs a byte before its last dot.
	for dot := after + 1; dot < run; dot++ {
		if text[dot] != '.' {
			continue
		}
		letters := 0
		for i := dot + 1; ; {
			if letters >= 2 && i > end && wordBoundary(text, i) {
				end = i
			}
			if i == run {
				break
			}
			if isASCIILetter(text[i]) {
				i++
			} else if n := unicodeLetterAt(text, i); n > 0 {
				i += n
			} else {
				break
			}
			letters++
		}
	}
	if end < 0 {
		return 0, 0, false
	}
	return start, end, true
}

// wordBoundary reports whether a word boundary holds before text[i]: exactly
// one side is a word character, an ASCII word byte or a Unicode letter.
func wordBoundary(text []byte, i int) bool {
	before := isWordByte(text[i-1]) || unicodeLetterBefore(text, i) > 0
	after := i < len(text) && (isWordByte(text[i]) || unicodeLetterAt(text, i) > 0)
	return before != after
}

func isWordByte(b byte) bool { return isASCIILetter(b) || isASCIIDigit(b) || b == '_' }

// unicodeLetterAt and unicodeLetterBefore return the byte length of a
// non-ASCII letter or combining mark starting at, or ending at, text[i], or
// 0. This covers "K" (U+212A) and "ſ" (U+017F), which (?i) folds to ASCII
// letters, and the letters of internationalised addresses.
func unicodeLetterAt(text []byte, i int) int {
	if i >= len(text) || text[i] < utf8.RuneSelf {
		return 0
	}
	r, n := utf8.DecodeRune(text[i:])
	if r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsMark(r)) {
		return n
	}
	return 0
}

func unicodeLetterBefore(text []byte, i int) int {
	if i <= 0 || text[i-1] < utf8.RuneSelf {
		return 0
	}
	r, n := utf8.DecodeLastRune(text[:i])
	if r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsMark(r)) {
		return n
	}
	return 0
}

// maskEmails masks, in out, every address in text in full. The rule's
// window is bounded, so in a stream an address longer than it is masked
// only within it; with the whole text in hand nothing is cut. Masking is
// the format-preserving mask, which leaves masked bytes as they are. It
// returns how many addresses it found that nothing had masked yet, and stops
// with ctx's error if ctx ends.
func maskEmails(ctx context.Context, text string, out []byte) (int, error) {
	src := []byte(text)
	found := 0
	err := eachEmail(ctx, src, func(start, end int) {
		if bytes.Equal(out[start:end], src[start:end]) {
			found++
		}
		for i := start; i < end; i++ {
			out[i] = formatMask(src[i])
		}
	})
	return found, err
}

// eachEmail calls visit with the offsets of every address in src, in full,
// and stops with ctx's error if ctx ends.
func eachEmail(ctx context.Context, src []byte, visit func(start, end int)) error {
	var prev localScan
	for i := 0; i < len(src); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		width := 0
		switch {
		case src[i] == '@':
			width = 1
		case src[i] == '%' && i+3 <= len(src) && src[i+1] == '4' && src[i+2] == '0':
			width = 3
		}
		if width == 0 {
			continue
		}
		if start, end, ok := matchEmail(src, i, i+width, false, &prev); ok {
			visit(start, end)
		}
	}
	return nil
}

// formatMask is goredact's format-preserving mask of one byte.
func formatMask(b byte) byte {
	switch {
	case b >= 'A' && b <= 'Z':
		return 'X'
	case b >= 'a' && b <= 'z':
		return 'x'
	case isASCIIDigit(b):
		return '9'
	case strings.IndexByte("-_.:/+=@", b) >= 0:
		return b
	}
	return '*'
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
func isASCIIDigit(b byte) bool  { return b >= '0' && b <= '9' }

func isLocalPartByte(b byte) bool {
	return isASCIILetter(b) || isASCIIDigit(b) || strings.IndexByte("._%+-", b) >= 0
}

func isDomainByte(b byte) bool {
	return isASCIILetter(b) || isASCIIDigit(b) || b == '.' || b == '-'
}

// Summary counts what one redaction masked.
type Summary struct {
	// Findings is the number of spans masked, ByRule the same split by rule
	// id; e-mail addresses are under EmailRuleID.
	Findings int
	ByRule   map[string]int
}

// Add folds other into s.
func (s *Summary) Add(other Summary) {
	s.Findings += other.Findings
	for rule, n := range other.ByRule {
		if s.ByRule == nil {
			s.ByRule = map[string]int{}
		}
		s.ByRule[rule] += n
	}
}

// Engine redacts with Config. It is immutable and safe for concurrent use.
type Engine struct {
	inner *goredact.Engine
	// finders holds idle engines that report findings for FindContext; one
	// is compiled whenever every other is busy.
	finders chan *finder
}

// finder is a goredact engine whose OnFinding collects into found, used by
// one call at a time.
type finder struct {
	engine *goredact.Engine
	found  []foundSpan
}

// foundSpan is one finding of the engine: the rule and the bytes it masks.
type foundSpan struct {
	rule       string
	start, end int
}

// echoRules are the rules that match the syntax around a value, such as
// "Bearer", "--password=" or "user:...@". Input that was redacted before
// holds that syntax around a value already masked, which the rules match
// again.
var echoRules = map[string]bool{
	"url-credentials":            true,
	"authorization-bearer":       true,
	"command-line-password-flag": true,
}

// alreadyMasked reports whether value is what the format-preserving mask
// leaves of a secret and nothing else: only X, x, 9 and *, with the
// separators the mask keeps, and at least one masked byte. Masking it again
// changes no byte, so it is no new finding. A value that merely starts like
// a mask, such as "xxxx-real", is not.
func alreadyMasked(value string) bool {
	masked := false
	for i := 0; i < len(value); i++ {
		switch b := value[i]; {
		case b == 'X' || b == 'x' || b == '9' || b == '*':
			masked = true
		case formatMask(b) == b:
		default:
			return false
		}
	}
	return masked
}

// scan redacts text into out with a borrowed finder and returns its
// findings, leaving out the ones of echoRules over a value that is already
// masked, and how many of them it left out per rule.
func (e *Engine) scan(ctx context.Context, text string, out io.Writer) (goredact.Stats, []foundSpan, map[string]int, error) {
	var f *finder
	select {
	case f = <-e.finders:
	default:
		var err error
		if f, err = newFinder(); err != nil {
			return goredact.Stats{}, nil, nil, err
		}
	}
	defer func() {
		f.found = nil
		select {
		case e.finders <- f:
		default:
		}
	}()
	stats, err := f.engine.Redact(ctx, out, strings.NewReader(text))
	if err != nil {
		return goredact.Stats{}, nil, nil, fmt.Errorf("redact: %w", err)
	}
	var kept []foundSpan
	var skipped map[string]int
	for _, found := range f.found {
		if echoRules[found.rule] && found.start >= 0 && found.end <= len(text) && alreadyMasked(text[found.start:found.end]) {
			if skipped == nil {
				skipped = map[string]int{}
			}
			skipped[found.rule]++
			continue
		}
		kept = append(kept, found)
	}
	return stats, kept, skipped, nil
}

// Span is the byte offsets [Start, End) of one secret or address the engine
// masks; Email marks an address.
type Span struct {
	Start, End int
	Email      bool
}

// New compiles the redaction rules.
func New() (*Engine, error) {
	inner, err := goredact.New(Config)
	if err != nil {
		return nil, fmt.Errorf("redact: constructing engine: %w", err)
	}
	return &Engine{inner: inner, finders: make(chan *finder, 4)}, nil
}

func newFinder() (*finder, error) {
	f := &finder{}
	config := Config
	config.RecordAligned = false
	config.OnFinding = func(found goredact.Finding) {
		f.found = append(f.found, foundSpan{rule: found.RuleID, start: int(found.Start), end: int(found.End)})
	}
	engine, err := goredact.New(config)
	if err != nil {
		return nil, fmt.Errorf("redact: constructing engine: %w", err)
	}
	f.engine = engine
	return f, nil
}

// FindContext returns, in input order, the spans RedactContext masks in
// text, one per finding: two secrets side by side are two spans however
// little lies between them. An e-mail address is reported in full, and
// may also be reported as the rule's window saw it. A value that is already
// masked, behind "Bearer", "--password=" or "user:", is no finding. It stops
// with ctx's error if ctx ends first.
func (e *Engine) FindContext(ctx context.Context, text string) ([]Span, error) {
	if text == "" {
		return nil, nil
	}
	_, found, _, err := e.scan(ctx, text, io.Discard)
	if err != nil {
		return nil, err
	}
	spans := make([]Span, len(found))
	for i, f := range found {
		spans[i] = Span{Start: f.start, End: f.end, Email: f.rule == EmailRuleID}
	}
	err = eachEmail(ctx, []byte(text), func(start, end int) { spans = append(spans, Span{Start: start, End: end, Email: true}) })
	if err != nil {
		return nil, fmt.Errorf("redact: %w", err)
	}
	return spans, nil
}

// Profile describes the configuration for a document's generator notes.
func (e *Engine) Profile() string {
	return fmt.Sprintf("goredact %s %s (%s)", Config.Profile, Config.MaskStrategy, e.inner.RuleSetVersion())
}

// RedactReader streams src to dst with every secret and e-mail address
// masked. The stream is otherwise byte for byte the input. An address with
// more than 254 bytes on one side of its @ is masked only within those 254
// bytes; Redact and RedactContext mask it in full. A stream is not held, so
// its Summary also counts a value that was already masked, which Redact
// leaves out.
func (e *Engine) RedactReader(ctx context.Context, dst io.Writer, src io.Reader) (Summary, error) {
	stats, err := e.inner.Redact(ctx, dst, src)
	if err != nil {
		return Summary{}, fmt.Errorf("redact: %w", err)
	}
	return fromStats(stats), nil
}

// Redact is RedactContext without cancellation.
func (e *Engine) Redact(text string) (string, Summary, error) {
	return e.RedactContext(context.Background(), text)
}

// RedactContext masks every secret and e-mail address in text, stopping
// with ctx's error if ctx ends first. The result has the same length and
// the same lines as the input: secrets are found over the whole text, so a
// key spanning several lines is caught, and the newlines the mask overwrote
// are put back. An e-mail address is masked in full however long it is.
func (e *Engine) RedactContext(ctx context.Context, text string) (string, Summary, error) {
	if text == "" {
		return text, Summary{}, nil
	}
	var out bytes.Buffer
	out.Grow(len(text))
	stats, _, skipped, err := e.scan(ctx, text, &out)
	if err != nil {
		return "", Summary{}, err
	}
	masked := out.Bytes()
	summary := fromStats(stats)
	for rule, n := range skipped {
		summary.Findings -= n
		if summary.ByRule[rule] -= n; summary.ByRule[rule] <= 0 {
			delete(summary.ByRule, rule)
		}
	}
	if len(masked) == len(text) {
		for i := 0; i < len(text); i++ {
			if text[i] == '\n' {
				masked[i] = '\n'
			}
		}
		found, err := maskEmails(ctx, text, masked)
		if err != nil {
			return "", Summary{}, fmt.Errorf("redact: %w", err)
		}
		// Addresses the engine's window left untouched are new findings.
		if found > 0 {
			if summary.ByRule == nil {
				summary.ByRule = map[string]int{}
			}
			summary.ByRule[EmailRuleID] += found
			summary.Findings += found
		}
	}
	return string(masked), summary, nil
}

var mentionPattern = regexp.MustCompile(`(^|[^A-Za-z0-9@/])@[A-Za-z0-9][A-Za-z0-9-]*`)

// StripMentions turns @-mentions into plain names and returns how many it
// rewrote. It cannot tell a person from an annotation such as @Override, so
// apply it only to text known to be conversation, never to anything that
// may hold code.
func StripMentions(text string) (string, int) {
	count := 0
	cleaned := mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		count++
		return strings.Replace(match, "@", "", 1)
	})
	return cleaned, count
}

func fromStats(stats goredact.Stats) Summary {
	summary := Summary{Findings: int(stats.Findings)}
	if len(stats.ByRule) > 0 {
		summary.ByRule = make(map[string]int, len(stats.ByRule))
		for rule, n := range stats.ByRule {
			summary.ByRule[rule] = int(n)
		}
	}
	return summary
}
