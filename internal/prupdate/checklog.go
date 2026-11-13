package prupdate

import (
	"bytes"
	"context"
	"errors"
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
	// maxRedactLine is how much of a line is kept to be redacted, before the
	// line is cut to maxLogLine, so a secret that straddles the cut is found.
	maxRedactLine = 16 << 10
	// maxTailRead is how much of the end of a log is read: far more than the
	// tail needs, so long lines and a cut first line cost nothing.
	maxTailRead = 8 << 20
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
	// LogUnavailable: GitHub has no log for the check: the job may not have
	// run, or the log is older than the repository keeps logs.
	LogUnavailable = "unavailable"
	// LogTooLong: the log is too long for baum to read the end of.
	LogTooLong = "too_long"
)

// logConclusions are the conclusions of a GitHub Actions run whose job ran,
// so GitHub may have a log for it; a skipped job, a run that did not start,
// one waiting for approval and a stale one have none.
var logConclusions = map[string]bool{"failure": true, "success": true, "cancelled": true, "timed_out": true, "neutral": true}

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
		case unicode.IsControl(r), invisible(r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return -1
		}
		return r
	}, line)
}

// invisible reports a character that draws nothing but changes how text
// reads: format characters (bidirectional overrides and isolates, zero-width
// characters, the byte order mark), variation selectors and the other
// default-ignorables.
func invisible(r rune) bool {
	switch r {
	case 0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x3164, 0xFFA0:
		return true
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// logTail keeps the last lines of a log written to it, cleaned, never more
// than the bounds hold: it reads the log as it streams and drops what falls
// out of the tail. A line is kept up to maxRedactLine bytes, so it can be
// redacted whole before it is cut.
type logTail struct {
	partial  []byte
	overlong bool
	lines    []string
	// trimmed is true once a line fell out of the front of the tail.
	trimmed bool
}

func (t *logTail) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		chunk := rest
		if i >= 0 {
			chunk = rest[:i]
		}
		if room := maxRedactLine - len(t.partial); len(chunk) > room {
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
	}
	t.partial, t.overlong = t.partial[:0], false
	t.lines = append(t.lines, text)
	if len(t.lines) > 2*MaxLogLines {
		t.trimmed = true
		t.lines = append(t.lines[:0], t.lines[len(t.lines)-MaxLogLines:]...)
	}
}

// last is the last MaxLogLines lines, without trailing empty lines, and
// whether the log has more before them. When the log was read from the middle
// of the file (partial) its first line, which the read may have cut, is left
// out.
func (t *logTail) last(partial bool) ([]string, bool) {
	if len(t.partial) > 0 || t.overlong {
		t.line()
	}
	lines := t.lines
	if partial && !t.trimmed && len(lines) > 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	more := t.trimmed || partial
	if len(lines) > MaxLogLines {
		lines, more = lines[len(lines)-MaxLogLines:], true
	}
	return lines, more
}

// shape cuts each of the (redacted) lines to maxLogLine bytes and keeps the
// last whole lines within MaxLogBytes.
func shape(lines []string, truncated bool) (string, bool) {
	cut := make([]string, len(lines))
	for i, line := range lines {
		if len(line) > maxLogLine {
			line, truncated = strings.ToValidUTF8(line[:maxLogLine], "")+"…", true
		}
		cut[i] = line
	}
	size, start := 0, len(cut)
	for start > 0 && size+len(cut[start-1])+1 <= MaxLogBytes {
		start--
		size += len(cut[start]) + 1
	}
	if start > 0 {
		truncated = true
	}
	return strings.Join(cut[start:], "\n"), truncated
}

// CheckLog reads the tail of the log of one of the pull request's check
// runs from GitHub, when the person opens it: check is a check run ID of the
// pull request's current head, read here, so a request cannot name another
// check run. Only a completed GitHub Actions run that ran has a log; a
// finished log does not change, so its tail is kept (Cache), and nothing
// reads a log in the background. GitHub masks the secrets it knows; the tail
// is redacted as all text baum shows is.
func (s Service) CheckLog(ctx context.Context, repository github.Repository, number int, check int64) (CheckLog, error) {
	// The account generation is taken before the account's reader is: a
	// change of account after this keeps the read from being stored or
	// shared with a read under the new account.
	gen := s.Cache.generation()
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
		out.Reason, out.Message = LogNone, "GitHub keeps no log for this check: it is not a GitHub Actions job that ran. Its own page may show one."
		return out, nil
	case !found.HasLog:
		out.Reason, out.Message = LogRunning, "This check has not finished, so its log is not shown yet."
		return out, nil
	}
	key := logKey(repository, check)
	if cached, ok := s.Cache.cachedLog(key); ok {
		return cached, nil
	}
	// Opening the same log twice at once, under the same account, downloads
	// it once.
	return s.Cache.readLog(key, gen, func() (CheckLog, error) {
		var tail logTail
		status, partial, err := read.reader.DownloadTail(ctx, fmt.Sprintf("%s/actions/jobs/%d/logs", repositoryPath(repository), check), &tail, maxTailRead)
		switch {
		case errors.Is(err, github.ErrDownloadTooLarge):
			out.Reason, out.Message = LogTooLong, "This log is too long for baum to show the end of. Open it on GitHub."
			s.Cache.storeLog(key, gen, out)
			return out, nil
		case err != nil:
			return CheckLog{}, &Error{Failed, unreachable}
		case status == http.StatusNotFound || status == http.StatusGone:
			// Not kept: GitHub may still be writing it.
			out.Reason, out.Message = LogUnavailable, "GitHub has no log for this check: it may not have run, or its log is older than the repository keeps."
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
		lines, more := tail.last(partial)
		// The whole lines are redacted before they are cut, so a secret that
		// straddles the cut is masked.
		redactor, err := bundle.NewRedactor()
		if err == nil {
			lines, _, err = redactor.RedactLines(ctx, lines)
		}
		if err != nil {
			return CheckLog{}, &Error{Failed, "baum could not check this log for secrets, so it does not show it."}
		}
		text, truncated := shape(lines, more)
		out.Available, out.Log, out.Truncated = true, text, truncated
		s.Cache.storeLog(key, gen, out)
		return out, nil
	})
}
