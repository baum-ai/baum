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

	"github.com/lastpersonlabs/goredact"
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

// emailRule masks e-mail addresses. Every @ triggers it; the validator then
// looks for a local part before the @ and a dotted domain after it, never
// crossing a newline.
var emailRule = goredact.CustomRule{
	ID:            EmailRuleID,
	Triggers:      []string{"@"},
	Confidence:    goredact.ConfidenceMedium,
	MaxLookbehind: emailLookbehind,
	MaxLookahead:  emailLookahead,
	Validate:      validateEmail,
}

func validateEmail(window []byte, trigStart, trigEnd int) (start, end int, ok bool) {
	return matchEmail(window, trigStart, trigEnd, trigEnd+emailLookahead == len(window))
}

// matchEmail finds the address around the @ at text[at:after]. It masks at
// least what the pattern
//
//	(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b
//
// matches: the whole run of local-part bytes before the @, and the domain
// up to the furthest label of two or more letters that ends on a word
// boundary, so a trailing ".1", "-1" or "2" leaves the address itself
// masked. Under (?i), "K" (U+212A) and "ſ" (U+017F) count as letters.
// When the domain runs to the end of a window cut short (cut), the
// address may go on past it, so the whole visible run is masked.
func matchEmail(text []byte, at, after int, cut bool) (start, end int, ok bool) {
	start = at
	for start > 0 {
		if isLocalPartByte(text[start-1]) {
			start--
		} else if n := foldedLetterBefore(text, start); n > 0 {
			start -= n
		} else {
			break
		}
	}
	if start == at {
		return 0, 0, false
	}
	run := after
	for run < len(text) {
		if isDomainByte(text[run]) {
			run++
		} else if n := foldedLetterAt(text, run); n > 0 {
			run += n
		} else {
			break
		}
	}
	if cut && run == len(text) {
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
			} else if n := foldedLetterAt(text, i); n > 0 {
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

// wordBoundary reports whether \b holds before text[i]: exactly one side is
// an ASCII word byte.
func wordBoundary(text []byte, i int) bool {
	return isWordByte(text[i-1]) != (i < len(text) && isWordByte(text[i]))
}

func isWordByte(b byte) bool { return isASCIILetter(b) || isASCIIDigit(b) || b == '_' }

// foldedLetterAt and foldedLetterBefore return the byte length of a "K"
// (U+212A) or "ſ" (U+017F) starting at, or ending at, text[i], or 0.
func foldedLetterAt(text []byte, i int) int {
	switch {
	case i+3 <= len(text) && text[i] == 0xE2 && text[i+1] == 0x84 && text[i+2] == 0xAA:
		return 3
	case i+2 <= len(text) && text[i] == 0xC5 && text[i+1] == 0xBF:
		return 2
	}
	return 0
}

func foldedLetterBefore(text []byte, i int) int {
	switch {
	case i >= 3 && text[i-3] == 0xE2 && text[i-2] == 0x84 && text[i-1] == 0xAA:
		return 3
	case i >= 2 && text[i-2] == 0xC5 && text[i-1] == 0xBF:
		return 2
	}
	return 0
}

// maskEmails masks, in out, every address in text in full. The rule's
// window is bounded, so in a stream an address longer than it is masked
// only within it; with the whole text in hand nothing is cut. Masking is
// the format-preserving mask, which leaves masked bytes as they are.
func maskEmails(text string, out []byte) {
	src := []byte(text)
	for at := bytes.IndexByte(src, '@'); at >= 0; {
		if start, end, ok := matchEmail(src, at, at+1, false); ok {
			for i := start; i < end; i++ {
				out[i] = formatMask(src[i])
			}
		}
		next := bytes.IndexByte(src[at+1:], '@')
		if next < 0 {
			break
		}
		at += 1 + next
	}
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
}

// New compiles the redaction rules.
func New() (*Engine, error) {
	inner, err := goredact.New(Config)
	if err != nil {
		return nil, fmt.Errorf("redact: constructing engine: %w", err)
	}
	return &Engine{inner: inner}, nil
}

// Profile describes the configuration for a document's generator notes.
func (e *Engine) Profile() string {
	return fmt.Sprintf("goredact %s %s (%s)", Config.Profile, Config.MaskStrategy, e.inner.RuleSetVersion())
}

// RedactReader streams src to dst with every secret and e-mail address
// masked. The stream is otherwise byte for byte the input. An address with
// more than 254 bytes on one side of its @ is masked only within those 254
// bytes; Redact and RedactContext mask it in full.
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
	stats, err := e.inner.Redact(ctx, &out, strings.NewReader(text))
	if err != nil {
		return "", Summary{}, fmt.Errorf("redact: %w", err)
	}
	masked := out.Bytes()
	if len(masked) == len(text) {
		for i := 0; i < len(text); i++ {
			if text[i] == '\n' {
				masked[i] = '\n'
			}
		}
		maskEmails(text, masked)
	}
	return string(masked), fromStats(stats), nil
}

var mentionPattern = regexp.MustCompile(`(^|[^\w@/])@[A-Za-z0-9][A-Za-z0-9-]*`)

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
