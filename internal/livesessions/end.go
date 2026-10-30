package livesessions

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Ender stops the agent of one of its sessions, by the session's agent and
// id, as if its user quit it: the agent's own end hook then says it ended.
// It returns ErrAgentGone when no process of the session is running.
type Ender func(ctx context.Context, id string) error

// ErrAgentGone is a session whose agent no longer runs: it ended without its
// end event reaching the daemon.
var ErrAgentGone = errors.New("the session's agent is not running")

// errNotEndable is a session the app may not end: not one waiting on its
// user or quiet, or of an agent baum cannot stop.
var errNotEndable = errors.New("the session is not waiting")

// endable reports whether the app may end the session: it waits on its user
// or went quiet (settled), so stopping it cuts no work short, and its agent
// has an Ender.
func (t *Transcripts) endable(s Session) bool {
	return s.State == StateIdle && s.WaitingReason != "" && t.Enders[s.Agent] != nil
}

// end stops a listed session's agent. A session whose agent is already gone
// is recorded as ended, so it leaves Needs you all the same.
func (t *Transcripts) end(ctx context.Context, agent, id string) error {
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
	case !found.Endable:
		return errNotEndable
	}
	err := t.Enders[agent](ctx, id)
	if errors.Is(err, ErrAgentGone) && t.Tracker.Gone(agent, id, time.Now()) {
		t.Tracker.signal()
		return nil
	}
	return err
}

// handleEnd serves POST /api/live-sessions/{agent}/{id}/end: 202 once the
// agent was asked to stop (the list shows it ended when its end hook
// arrives), 404 for a session not listed, 409 for one not waiting or quiet,
// or of an agent baum cannot stop, and 500 when stopping it failed.
func (t *Transcripts) handleEnd(w http.ResponseWriter, r *http.Request) {
	err := t.end(r.Context(), r.PathValue("agent"), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]bool{"ending": true})
	case errors.Is(err, ErrNotTracked):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
	case errors.Is(err, errNotEndable):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "session_not_waiting", "message": "Only a session waiting on you or gone quiet can be ended."})
	default:
		t.logger().Warn("live session not ended", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "session_not_ended", "message": "The session could not be ended."})
	}
}
