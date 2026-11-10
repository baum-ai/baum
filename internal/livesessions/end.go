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

// ErrRunningCommand is a session that waits on its user while a background
// command it started still runs (Claude Code's registry says "shell"):
// ending it would kill the command, so it is left to finish or be stopped in
// the session.
var ErrRunningCommand = errors.New("a background command is still running in the session")

// EndCheck reports whether a session of its agent may be ended now, by the
// session's id: nil, or why its Ender would refuse (errNotEndable,
// ErrRunningCommand). Any other error, such as a process that is gone, is
// left for the Ender to settle.
type EndCheck func(id string) error

// endable reports whether the app may end the session (endRefusal).
func (t *Transcripts) endable(s Session) bool { return t.endRefusal(s) == nil }

// endRefusal is why the app may not end the session, or nil when it may: it
// waits on its user or went quiet (settled), so stopping it cuts no work
// short, its agent has an Ender, and that agent's EndCheck, where it has
// one, does not refuse it. The list then offers End only where ending would
// go through.
func (t *Transcripts) endRefusal(s Session) error {
	if s.State != StateIdle || s.WaitingReason == "" || t.Enders[s.Agent] == nil {
		return errNotEndable
	}
	check := t.EndChecks[s.Agent]
	if check == nil {
		return nil
	}
	switch err := check(s.ID); {
	case errors.Is(err, ErrRunningCommand), errors.Is(err, errNotEndable):
		return err
	}
	return nil
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
	if found == nil {
		return ErrNotTracked
	}
	// Asked again rather than read from found.Endable, to say why.
	if err := t.endRefusal(found.Session); err != nil {
		return err
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
// of an agent baum cannot stop, or running a background command, and 500
// when stopping it failed.
func (t *Transcripts) handleEnd(w http.ResponseWriter, r *http.Request) {
	err := t.end(r.Context(), r.PathValue("agent"), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]bool{"ending": true})
	case errors.Is(err, ErrNotTracked):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "live_session_not_found", "message": "This session is no longer listed."})
	case errors.Is(err, ErrRunningCommand):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "session_running_command", "message": "A background command is still running in this session. Stop it in the session, then end it."})
	case errors.Is(err, errNotEndable):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "session_not_waiting", "message": "Only a session waiting on you or gone quiet can be ended."})
	default:
		t.logger().Warn("live session not ended", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "session_not_ended", "message": "The session could not be ended."})
	}
}
