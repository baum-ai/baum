package prupdate

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
)

// Bounds on the log tail the app shows of a failing check.
const (
	// MaxLogLines and MaxLogBytes bound the tail; a line is cut at
	// maxLogLine bytes.
	MaxLogLines = 200
	MaxLogBytes = 64 << 10
	maxLogLine  = 2 << 10
	// logCacheSize is how many check runs' log tails the cache keeps.
	logCacheSize = 64
)

// The reasons a check has no log tail to show.
const (
	// LogNone: no log is kept for the check (another app's check run, or a
	// commit status).
	LogNone = "no_log"
	// LogRunning: the check has not completed, so its log is still growing.
	LogRunning = "running"
	// LogExpired: GitHub no longer keeps the log.
	LogExpired = "expired"
)

// CheckLog is the tail of a check's log, or why there is none. Log is the
// check's own output and untrusted.
type CheckLog struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Available: Log holds the tail; else Reason says why not.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	Log       string `json:"log,omitempty"`
	// Truncated: the log is longer than the tail.
	Truncated bool   `json:"truncated,omitempty"`
	URL       string `json:"url,omitempty"`
}

var (
	// logTimestamp is the time GitHub Actions puts before every log line.
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)
	// logEscape is a terminal colour or cursor sequence.
	logEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
)

// cleanLogLine drops a line's timestamp, terminal escapes and control
// characters (a tab becomes a space) and its invalid UTF-8.
func cleanLogLine(line string) string {
	line = strings.ToValidUTF8(line, "")
	line = logEscape.ReplaceAllString(logTimestamp.ReplaceAllString(strings.TrimPrefix(line, "\xef\xbb\xbf"), ""), "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r), r == 0xFEFF, r == 0x2028, r == 0x2029:
			return -1
		}
		return r
	}, line)
}

// logTail keeps the last lines of a log written to it, cleaned, never more
// than the bounds hold: it reads the log as it streams and drops what falls
// out of the tail.
type logTail struct {
	partial  []byte
	overlong bool
	lines    []string
	// dropped is true once a line fell out of the tail or a line was cut.
	dropped bool
}

func (t *logTail) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		chunk := rest
		if i >= 0 {
			chunk = rest[:i]
		}
		if room := maxLogLine - len(t.partial); len(chunk) > room {
			chunk, t.overlong = chunk[:max(room, 0)], true
		}
		t.partial = append(t.partial, chunk...)
		if i < 0 {
			break
		}
		t.line()
		rest = rest[i+1:]
	}
	return len(p), nil
}

func (t *logTail) line() {
	text := cleanLogLine(string(t.partial))
	if t.overlong {
		text += "…"
		t.dropped = true
	}
	t.partial, t.overlong = t.partial[:0], false
	t.lines = append(t.lines, text)
	if len(t.lines) > 2*MaxLogLines {
		t.dropped = true
		t.lines = append(t.lines[:0], t.lines[len(t.lines)-MaxLogLines:]...)
	}
}

// tail is the kept lines: at most MaxLogLines and MaxLogBytes bytes, whole
// lines, the last ones, without trailing empty lines.
func (t *logTail) tail() (string, bool) {
	if len(t.partial) > 0 || t.overlong {
		t.line()
	}
	lines := t.lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	truncated := t.dropped
	if len(lines) > MaxLogLines {
		lines, truncated = lines[len(lines)-MaxLogLines:], true
	}
	size, start := 0, len(lines)
	for start > 0 && size+len(lines[start-1])+1 <= MaxLogBytes {
		start--
		size += len(lines[start]) + 1
	}
	if start > 0 {
		truncated = true
	}
	return strings.Join(lines[start:], "\n"), truncated
}

// CheckLog reads the tail of the log of one of the pull request's check
// runs from GitHub, when the person opens it: check is a check run ID of the
// pull request's current head, read here, so a request cannot name another
// check run. Only a completed GitHub Actions run has a log; a finished log
// does not change, so its tail is kept (Cache), and nothing reads a log in
// the background. GitHub masks the secrets it knows; the tail is redacted as
// all text baum shows is.
func (s Service) CheckLog(ctx context.Context, repository github.Repository, number int, check int64) (CheckLog, error) {
	read, err := s.open(ctx, repository, number)
	if err != nil {
		return CheckLog{}, err
	}
	list, err := s.AllChecks(ctx, repository, number, read.pull.HeadSHA)
	if err != nil {
		return CheckLog{}, err
	}
	var found *CheckStatus
	for i := range list.Checks {
		if check > 0 && list.Checks[i].ID == check {
			found = &list.Checks[i]
			break
		}
	}
	if found == nil {
		return CheckLog{}, &Error{NotFound, "This check is not one of this pull request's checks."}
	}
	out := CheckLog{ID: check, Name: found.Name, URL: found.URL}
	switch {
	case !found.HasLog && found.Status == "completed":
		out.Reason, out.Message = LogNone, "GitHub keeps no log for this check: it is not a GitHub Actions run. Its own page may show one."
		return out, nil
	case !found.HasLog:
		out.Reason, out.Message = LogRunning, "This check has not finished, so its log is not shown yet."
		return out, nil
	}
	key, gen := logKey(repository, check), s.Cache.generation()
	if cached, ok := s.Cache.cachedLog(key); ok {
		return cached, nil
	}
	var tail logTail
	status, err := read.reader.Download(ctx, fmt.Sprintf("%s/actions/jobs/%d/logs", repositoryPath(repository), check), &tail)
	switch {
	case err != nil:
		return CheckLog{}, &Error{Failed, unreachable}
	case status == http.StatusNotFound || status == http.StatusGone:
		out.Reason, out.Message = LogExpired, "GitHub no longer has this log: it is older than the repository keeps logs."
		s.Cache.storeLog(key, gen, out)
		return out, nil
	case status == http.StatusUnauthorized:
		return CheckLog{}, &Error{Forbidden, reconnect}
	case status == http.StatusTooManyRequests:
		return CheckLog{}, &Error{Failed, rateLimited}
	case status == http.StatusForbidden:
		return CheckLog{}, &Error{Failed, "GitHub would not show this log. Your account may lack access to GitHub Actions logs here, or GitHub is limiting requests."}
	case status != http.StatusOK:
		return CheckLog{}, &Error{Failed, fmt.Sprintf("GitHub could not show this log (HTTP %d).", status)}
	}
	text, truncated := tail.tail()
	redactor, err := bundle.NewRedactor()
	if err == nil {
		text, _, err = redactor.RedactString(ctx, text)
	}
	if err != nil {
		return CheckLog{}, &Error{Failed, "baum could not check this log for secrets, so it does not show it."}
	}
	out.Available, out.Log, out.Truncated = true, text, truncated
	s.Cache.storeLog(key, gen, out)
	return out, nil
}
