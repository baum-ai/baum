package livesessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Sender puts a message the user wrote in the app into one of its agent's
// sessions, by id, as a new turn of the same conversation, shown in the
// session's own terminal. It returns ErrAgentGone when no process of the
// session is running.
type Sender func(ctx context.Context, id, text string) error

// maxMessage bounds a message from the app, in characters.
const maxMessage = 10000

// fromBaum is how a message the app sends opens, so the agent knows its user
// sent it: Claude Code tells its model a message on its inbox came from
// another session.
const fromBaum = "Your user sent this from the baum desktop app, in place of typing it in this terminal:"

// framed is a message as an agent receives it.
func framed(text string) string { return fromBaum + "\n\n" + text }

// errNotMessageable is a session the app may not message.
var errNotMessageable = errors.New("the session is not waiting at its prompt")

// messageable reports whether the app may message the session: it waits at
// its prompt, after a finished turn or (Claude Code, whose process baum
// finds) gone quiet, and its agent has a Sender. A session asking a
// permission or a question is answered, not messaged, and a quiet Codex
// session may have no terminal left to show the turn in.
func (t *Transcripts) messageable(s Session) bool {
	if s.State != StateIdle || t.Senders[s.Agent] == nil {
		return false
	}
	return s.WaitingReason == WaitTurnFinished || (s.WaitingReason == WaitQuiet && s.Agent == AgentClaudeCode)
}

// message sends text to a listed session. A session whose agent is gone is
// recorded as ended.
func (t *Transcripts) message(ctx context.Context, agent, id, text string) error {
	var found *Listed
	for _, l := range t.listed() {
		if l.Agent == agent && l.ID == id {
			found = &l
			break
		}
	}
	switch {
	case found == nil:
		return ErrNotTracked
	case !found.Messageable:
		return errNotMessageable
	}
	err := t.Senders[agent](ctx, id, framed(text))
	if errors.Is(err, ErrAgentGone) && t.Tracker.Gone(agent, id, time.Now()) {
		t.Tracker.signal()
	}
	return err
}

// handleMessage serves POST /api/live-sessions/{agent}/{id}/message with
// {"text"}: 202 once the agent has the message (the list shows it working
// when its prompt hook arrives), 400 for an empty or overlong text, 404 for a
// session not listed, 409 for one not waiting at its prompt, 410 for one whose
// agent is gone (recorded as ended), and 500 when sending failed.
func (t *Transcripts) handleMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*maxMessage+1024)).Decode(&body); err != nil ||
		strings.TrimSpace(body.Text) == "" || utf8.RuneCountInString(body.Text) > maxMessage {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "message_invalid", "message": "Write a message of at most 10,000 characters."})
		return
	}
	err := t.message(r.Context(), r.PathValue("agent"), r.PathValue("id"), strings.TrimSpace(body.Text))
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]bool{"sent": true})
	case errors.Is(err, ErrNotTracked):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
	case errors.Is(err, errNotMessageable):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "session_not_at_prompt", "message": "Only a session waiting at its prompt can be messaged."})
	case errors.Is(err, ErrAgentGone):
		writeJSON(w, http.StatusGone, map[string]any{"code": "agent_gone", "message": "The session is not running any more."})
	default:
		t.logger().Warn("live session not messaged", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "message_not_sent", "message": "The message could not be sent."})
	}
}
