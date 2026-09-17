// Package redact removes secrets and personal identities from text before it
// is shown, stored or sent to a model provider.
//
// Secrets are found by goredact's balanced profile and masked in place with a
// format-preserving mask, so a masked value keeps its length and shape and the
// text around it, including line numbers in a patch, stays valid. Identities
// are e-mail addresses and @-mentions.
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

// Config is how every byte reviewpal redacts is redacted. RecordAligned keeps
// emitted output on newline boundaries so JSONL streams stay whole records.
var Config = goredact.Config{
	Profile:       goredact.ProfileBalanced,
	MaskStrategy:  goredact.MaskFormatPreserving,
	RecordAligned: true,
}

// Marker replaces an e-mail address.
const Marker = "[redacted]"

var (
	emailPattern   = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	mentionPattern = regexp.MustCompile(`(^|[^\w@/])@[A-Za-z0-9][A-Za-z0-9-]*`)
)

// Summary counts what one redaction masked.
type Summary struct {
	// Secrets is the number of secret spans masked, ByRule the same split by
	// goredact rule id.
	Secrets int
	ByRule  map[string]int
	// Identities is the number of e-mail addresses and mentions rewritten.
	Identities int
}

// Total is every span this summary rewrote.
func (s Summary) Total() int { return s.Secrets + s.Identities }

// Add folds other into s.
func (s *Summary) Add(other Summary) {
	s.Secrets += other.Secrets
	s.Identities += other.Identities
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

// RedactReader streams src to dst with secrets masked. Identities are not
// rewritten on this path: it is for whole files whose structure must survive
// byte for byte apart from the masks.
func (e *Engine) RedactReader(ctx context.Context, dst io.Writer, src io.Reader) (Summary, error) {
	stats, err := e.inner.Redact(ctx, dst, src)
	if err != nil {
		return Summary{}, fmt.Errorf("redact: %w", err)
	}
	return fromStats(stats), nil
}

// RedactString masks secrets and rewrites identities in prose: prompts,
// titles, comment bodies, commit messages.
func (e *Engine) RedactString(text string) (string, Summary, error) {
	masked, summary, err := e.secrets(text)
	if err != nil {
		return "", Summary{}, err
	}
	masked, identities := RedactIdentities(masked)
	summary.Identities = identities
	return masked, summary, nil
}

// RedactLines masks secrets and e-mail addresses while keeping every line
// where it was, so a patch hunk's line numbers stay valid. Secrets are found
// over the whole text, so a key spanning several lines is still caught; the
// format-preserving mask keeps every byte offset, and the newlines it would
// overwrite are put back. Mentions are left alone: in code, @ introduces
// annotations and decorators far more often than people.
func (e *Engine) RedactLines(text string) (string, Summary, error) {
	masked, summary, err := e.secrets(text)
	if err != nil {
		return "", Summary{}, err
	}
	if len(masked) == len(text) {
		buf := []byte(masked)
		for i := 0; i < len(text); i++ {
			if text[i] == '\n' {
				buf[i] = '\n'
			}
		}
		masked = string(buf)
	} else {
		// The mask was not length-preserving; redact line by line instead.
		lines := strings.Split(text, "\n")
		summary = Summary{}
		for i, line := range lines {
			var lineSummary Summary
			if lines[i], lineSummary, err = e.secrets(line); err != nil {
				return "", Summary{}, err
			}
			summary.Add(lineSummary)
		}
		masked = strings.Join(lines, "\n")
	}
	lines := strings.Split(masked, "\n")
	for i, line := range lines {
		var emails int
		lines[i], emails = redactEmails(line)
		summary.Identities += emails
	}
	return strings.Join(lines, "\n"), summary, nil
}

func (e *Engine) secrets(text string) (string, Summary, error) {
	if text == "" {
		return text, Summary{}, nil
	}
	var out bytes.Buffer
	out.Grow(len(text))
	stats, err := e.inner.Redact(context.Background(), &out, strings.NewReader(text))
	if err != nil {
		return "", Summary{}, fmt.Errorf("redact: %w", err)
	}
	return out.String(), fromStats(stats), nil
}

// RedactIdentities replaces e-mail addresses with Marker and turns @-mentions
// into plain names, returning how many it rewrote.
func RedactIdentities(text string) (string, int) {
	cleaned, count := redactEmails(text)
	cleaned = mentionPattern.ReplaceAllStringFunc(cleaned, func(match string) string {
		count++
		return strings.Replace(match, "@", "", 1)
	})
	return cleaned, count
}

func redactEmails(text string) (string, int) {
	count := 0
	cleaned := emailPattern.ReplaceAllStringFunc(text, func(string) string {
		count++
		return Marker
	})
	return cleaned, count
}

func fromStats(stats goredact.Stats) Summary {
	summary := Summary{Secrets: int(stats.Findings)}
	if len(stats.ByRule) > 0 {
		summary.ByRule = make(map[string]int, len(stats.ByRule))
		for rule, n := range stats.ByRule {
			summary.ByRule[rule] = int(n)
		}
	}
	return summary
}
