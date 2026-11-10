package fix

import (
	"bytes"
	"regexp"
	"strings"
)

// maxLogLine bounds one line of a job log excerpt; a longer line is cut and
// ends in "…".
const maxLogLine = 1 << 10

var (
	// logTimestamp is the time GitHub Actions puts before every log line.
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)
	// logEscape is a terminal colour or cursor sequence.
	logEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
)

// jobLog reads a GitHub Actions job log as it is written and keeps the lines
// that show why the job failed: the last MaxLogLines lines up to the end of
// the last step that logged an ##[error] line. The runner's post-job cleanup
// after a failed step can be hundreds of lines, so the plain end of the log
// often shows nothing of the failure. Errors of the job's steps win over
// errors of the cleanup ("Post job cleanup." onwards); a log without an
// error line gives its last lines. Timestamps and terminal escapes are left
// out, as they spend the bound without telling the agent anything.
type jobLog struct {
	partial  []byte
	overlong bool
	recent   []string
	failed   []string
	// failedInSteps reports that failed ends with a step of the job rather
	// than its cleanup.
	failedInSteps bool
	// errored is set by an error line in the current step, and errorInSteps
	// when that step is not part of the cleanup.
	errored, errorInSteps bool
	cleanup               bool
}

func (l *jobLog) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		chunk := rest
		if i >= 0 {
			chunk = rest[:i]
		}
		if room := maxLogLine - len(l.partial); len(chunk) > room {
			chunk, l.overlong = chunk[:max(room, 0)], true
		}
		l.partial = append(l.partial, chunk...)
		if i < 0 {
			break
		}
		l.line()
		rest = rest[i+1:]
	}
	return len(p), nil
}

func (l *jobLog) line() {
	text := strings.ToValidUTF8(strings.TrimSuffix(string(l.partial), "\r"), "")
	if l.overlong {
		text += "…"
	}
	l.partial, l.overlong = l.partial[:0], false
	text = logEscape.ReplaceAllString(logTimestamp.ReplaceAllString(text, ""), "")
	cleanup := text == "Post job cleanup."
	if strings.HasPrefix(text, "##[group]Run ") || cleanup {
		l.endStep()
		l.cleanup = l.cleanup || cleanup
	}
	l.recent = append(l.recent, text)
	if len(l.recent) > 2*MaxLogLines {
		l.recent = append(l.recent[:0], l.recent[len(l.recent)-MaxLogLines:]...)
	}
	if strings.HasPrefix(text, "##[error]") {
		l.errored, l.errorInSteps = true, l.errorInSteps || !l.cleanup
	}
}

// endStep keeps the lines up to here when the step that ends logged an error.
func (l *jobLog) endStep() {
	if !l.errored {
		return
	}
	if l.errorInSteps || !l.failedInSteps {
		l.failed, l.failedInSteps = append([]string(nil), last(l.recent, MaxLogLines)...), l.errorInSteps
	}
	l.errored, l.errorInSteps = false, false
}

// String is the excerpt, at most MaxLogLines lines and maxLogBytes bytes.
func (l *jobLog) String() string {
	if len(l.partial) > 0 || l.overlong {
		l.line()
	}
	l.endStep()
	lines := l.failed
	if lines == nil {
		lines = last(l.recent, MaxLogLines)
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	for len(text) > maxLogBytes {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			return strings.ToValidUTF8(text[len(text)-maxLogBytes:], "")
		}
		text = text[i+1:]
	}
	return text
}

func last(lines []string, n int) []string {
	return lines[max(len(lines)-n, 0):]
}
