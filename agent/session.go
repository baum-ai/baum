package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrSessionNotFound is a resume or fork whose session the CLI could not
// find: its files were deleted, or it was recorded under another working
// directory. The call made no model request, so nothing was spent.
var ErrSessionNotFound = errors.New("the agent session to continue was not found")

// sessionMissing reports, conservatively, whether a failed call failed only
// because the session it resumes is gone: it resumed one, made no model call
// and used no tokens, and the CLI's error names a missing session. Claude
// Code says "No conversation found with session ID"; Codex names the thread,
// session or rollout it could not find.
func sessionMissing(request Request, result Result, err error) bool {
	if err == nil || request.ResumeSession == "" || result.Calls > 0 || result.Tokens > 0 {
		return false
	}
	text := strings.ToLower(err.Error())
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

// Continue runs one call of a conversation that keeps its agent session
// (Request.PersistSession, ResumeSession and ForkSession) through the loop's
// provider retries, stall watchdog and budgets, charging its calls and tokens
// to outcome. Unlike RunTask it neither validates nor repairs the reply: a
// repair has to continue the same session, so the caller checks the reply and
// calls Continue again with the session the result names. A resume or fork
// whose session the CLI cannot find fails at once, without retries, with an
// error wrapping ErrSessionNotFound.
func (g *Generator) Continue(ctx context.Context, budgets Budgets, request Request, outcome *Outcome) (Result, error) {
	if err := request.ValidateSession(); err != nil {
		return Result{}, err
	}
	stalls := 0
	return g.call(ctx, budgets, request, outcome, &stalls)
}

// sessionNotFound wraps a CLI's missing-session error.
func sessionNotFound(err error) error { return fmt.Errorf("%w: %v", ErrSessionNotFound, err) }
