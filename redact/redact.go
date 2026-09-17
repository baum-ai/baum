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

// Config is how every byte reviewpal redacts is redacted. RecordAligned keeps
// emitted output on newline boundaries so JSONL streams stay whole records.
var Config = goredact.Config{
	Profile:       goredact.ProfileBalanced,
	MaskStrategy:  goredact.MaskFormatPreserving,
	RecordAligned: true,
	CustomRules:   []goredact.CustomRule{emailRule},
}

// emailRule masks e-mail addresses. Every @ triggers it; the validator then
// looks for a local part before the @ and a dotted domain after it, never
// crossing a newline. The bounds are the RFC 5321 limits on each side.
var emailRule = goredact.CustomRule{
	ID:            EmailRuleID,
	Triggers:      []string{"@"},
	Confidence:    goredact.ConfidenceMedium,
	MaxLookbehind: 64,
	MaxLookahead:  253,
	Validate:      validateEmail,
}

func validateEmail(window []byte, trigStart, trigEnd int) (start, end int, ok bool) {
	start = trigStart
	for start > 0 && isLocalPartByte(window[start-1]) {
		start--
	}
	if start == trigStart {
		return 0, 0, false
	}
	end = trigEnd
	for end < len(window) && isDomainByte(window[end]) {
		end++
	}
	// A sentence may end right after the address; the dot is not part of it.
	for end > trigEnd && (window[end-1] == '.' || window[end-1] == '-') {
		end--
	}
	domain := window[trigEnd:end]
	dot := bytes.LastIndexByte(domain, '.')
	if dot <= 0 || domain[0] == '.' || domain[0] == '-' || len(domain)-dot-1 < 2 {
		return 0, 0, false
	}
	for _, b := range domain[dot+1:] {
		if !isASCIILetter(b) {
			return 0, 0, false
		}
	}
	return start, end, true
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
// masked. The stream is otherwise byte for byte the input.
func (e *Engine) RedactReader(ctx context.Context, dst io.Writer, src io.Reader) (Summary, error) {
	stats, err := e.inner.Redact(ctx, dst, src)
	if err != nil {
		return Summary{}, fmt.Errorf("redact: %w", err)
	}
	return fromStats(stats), nil
}

// Redact masks every secret and e-mail address in text. The result has the
// same length and the same lines as the input: secrets are found over the
// whole text, so a key spanning several lines is caught, and the newlines
// the mask overwrote are put back.
func (e *Engine) Redact(text string) (string, Summary, error) {
	if text == "" {
		return text, Summary{}, nil
	}
	var out bytes.Buffer
	out.Grow(len(text))
	stats, err := e.inner.Redact(context.Background(), &out, strings.NewReader(text))
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
