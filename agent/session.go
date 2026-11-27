package agent

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSessionNotFound is a resume whose session the CLI could not
// find: its files were deleted, or it was recorded under another working
// directory. The call made no model request, so nothing was spent.
var ErrSessionNotFound = errors.New("the agent session to continue was not found")

// ValidSessionID reports whether id is a session id a request may resume: a
// canonical UUID, as both CLIs report.
func ValidSessionID(id string) bool { return sessionID.MatchString(id) }

// missingSessionText reports whether one line of a CLI's error output says
// the session it was asked to continue does not exist. Claude Code says "No
// conversation found with session ID"; Codex names the thread, session or
// rollout it could not find ("thread/resume failed: no rollout found for
// thread id ...").
func missingSessionText(line string) bool {
	text := strings.ToLower(line)
	if strings.Contains(text, "no conversation found") {
		return true
	}
	if !strings.Contains(text, "not found") && !strings.Contains(text, "no such") && !strings.Contains(text, "no saved") && !strings.Contains(text, "no rollout") {
		return false
	}
	for _, noun := range []string{"session", "thread", "rollout", "conversation"} {
		if strings.Contains(text, noun) {
			return true
		}
	}
	return false
}

// MissingSession finds the line of a CLI's error output, every line of it,
// that says the session to continue does not exist: a CLI may print warnings
// first.
func MissingSession(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if missingSessionText(line) {
			return strings.TrimSpace(line), true
		}
	}
	return "", false
}

// SessionNotFound is the error a backend returns for a resume whose session
// is gone, with the CLI's own line.
func SessionNotFound(cli, line string) error {
	return fmt.Errorf("%w: %s: %s", ErrSessionNotFound, cli, line)
}

// sessionMissing reports, conservatively, whether a failed call failed only
// because the session it resumes is gone: it resumed one, made no model call
// and used no tokens, and the error names a missing session. A backend that
// recognises the case itself returns ErrSessionNotFound.
func sessionMissing(request Request, result Result, err error) bool {
	if err == nil || request.ResumeSession == "" || result.Calls > 0 || result.Tokens > 0 {
		return false
	}
	_, missing := MissingSession(err.Error())
	return missing
}

// sessionNotFound wraps an error that names a missing session.
func sessionNotFound(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrSessionNotFound, err)
}
