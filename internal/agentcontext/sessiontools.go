package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The tools of the Sessions page and a session's page. live_sessions lists
// one repository's sessions when asked, so an action may read its own
// repository's; the transcripts and the agents' requests carry no repository,
// so they are global.

const (
	defaultPrompts = 20
	maxPrompts     = 100
)

func (s *Server) sessionTools() []Tool {
	return []Tool{{
		Name: "live_sessions",
		Description: "The agent sessions the Sessions page lists, the most recently active first: each one's agent, title, state (running, idle or ended), what it waits for (a permission, an answer, or its turn finished) and since when, what it is doing (its latest steps and message), and its repository, branch and pull request. " +
			"Pass repository to list one repository's.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"repository":{"type":"string","description":"Only this repository's sessions, as owner/name. An action's is always its own."},` +
			`"state":{"type":"string","enum":["running","idle","ended"],"description":"Only sessions in this state."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most to return. Default 50."}},"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/live-sessions"},
		Call:  callLiveSessions,
	}, {
		Name: "live_session_transcript",
		Description: "A running or ended session's transcript as the session page shows it: its prompts in order with the agent's replies, effects and cost, and the latest compaction summary. Images pasted with a prompt are never returned. " +
			"Defaults to the page's session. The prompts come limit at a time (default 20, at most 100) from turn from_turn; the result names the next turn.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"session":{"type":"string","description":"The session as agent:id: the agent and session_id of a live_sessions row joined by a colon. Default: the page's."},` +
			`"from_turn":{"type":"integer","minimum":1,"description":"The first prompt's ordinal to return. Default 1."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":100,"description":"The most prompts to return. Default 20."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/live-sessions"},
		Call:  callLiveSessionTranscript,
	}, {
		Name:        "agent_requests",
		Description: "What running agents wait to be asked of the person in the app: each pending permission request or question with its agent, session, tool, input and options. The app's settings for answering them are not part of it.",
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/agent-requests"},
		Call:        callAgentRequests,
	}, {
		Name:        "agent_messages",
		Description: "The messages the person queued for running agent sessions, which each receives when its turn ends: the session, the text and when it was queued.",
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/agent-requests"},
		Call:        callAgentMessages,
	}}
}

// sessionRow is a session as the Sessions page shows it. The head sha, the
// upstream and the hooks' own record of where the session runs are left out.
type sessionRow struct {
	Agent          string          `json:"agent"`
	SessionID      string          `json:"session_id"`
	Title          string          `json:"title,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	State          string          `json:"state"`
	StartedAt      string          `json:"started_at,omitempty"`
	LastActivityAt string          `json:"last_activity_at,omitempty"`
	LastPromptAt   string          `json:"last_prompt_at,omitempty"`
	EndedAt        string          `json:"ended_at,omitempty"`
	WaitingFor     string          `json:"waiting_for,omitempty"`
	WaitingSince   string          `json:"waiting_since,omitempty"`
	WaitingMessage string          `json:"waiting_message,omitempty"`
	Repository     string          `json:"repository,omitempty"`
	Branch         string          `json:"branch,omitempty"`
	PullRequest    int             `json:"pull_request,omitempty"`
	PullState      string          `json:"pull_request_state,omitempty"`
	Activity       json.RawMessage `json:"activity,omitempty"`
	Endable        bool            `json:"endable,omitempty"`
	Messageable    bool            `json:"messageable,omitempty"`
}

func callLiveSessions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
		State      string `json:"state"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository := ""
	if c.Consumer.Kind == Action || a.Repository != "" {
		var err error
		if repository, _, err = c.Target(a.Repository, 0); err != nil {
			return "", err
		}
	}
	body, _, err := c.get(ctx, "/api/live-sessions", nil)
	if err != nil {
		return "", fmt.Errorf("the sessions are unavailable: %w", err)
	}
	if disabled(body) {
		return "", errors.New("the sessions are unavailable: the daemon does not track them")
	}
	var list struct {
		Sessions []struct {
			Agent          string          `json:"agent"`
			SessionID      string          `json:"session_id"`
			Title          string          `json:"title"`
			Summary        string          `json:"summary"`
			State          string          `json:"state"`
			StartedAt      string          `json:"started_at"`
			LastActivityAt string          `json:"last_activity_at"`
			LastPromptAt   string          `json:"last_prompt_at"`
			EndedAt        string          `json:"ended_at"`
			WaitingReason  string          `json:"waiting_reason"`
			WaitingSince   string          `json:"waiting_since"`
			WaitingMessage string          `json:"waiting_message"`
			Repository     string          `json:"repository"`
			Branch         string          `json:"branch"`
			PR             int             `json:"pr"`
			PRState        string          `json:"pr_state"`
			Activity       json.RawMessage `json:"activity"`
			Endable        bool            `json:"endable"`
			Messageable    bool            `json:"messageable"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", errors.New("the sessions are not readable")
	}
	rows := []sessionRow{}
	for _, s := range list.Sessions {
		if (repository != "" && !strings.EqualFold(s.Repository, repository)) || (a.State != "" && s.State != a.State) {
			continue
		}
		rows = append(rows, sessionRow{Agent: s.Agent, SessionID: s.SessionID, Title: s.Title, Summary: s.Summary, State: s.State,
			StartedAt: zeroTime(s.StartedAt), LastActivityAt: zeroTime(s.LastActivityAt), LastPromptAt: zeroTime(s.LastPromptAt), EndedAt: zeroTime(s.EndedAt),
			WaitingFor: s.WaitingReason, WaitingSince: zeroTime(s.WaitingSince), WaitingMessage: s.WaitingMessage,
			Repository: s.Repository, Branch: s.Branch, PullRequest: s.PR, PullState: s.PRState,
			Activity: s.Activity, Endable: s.Endable, Messageable: s.Messageable})
	}
	limit := boundRows(a.Limit, defaultRows, maxRows)
	more := max(0, len(rows)-limit)
	total := len(rows)
	rows = rows[:min(len(rows), limit)]
	return compactJSON(struct {
		Total    int          `json:"total"`
		More     int          `json:"more,omitempty"`
		Sessions []sessionRow `json:"sessions"`
	}{total, more, rows})
}

// transcriptTarget is the agent and id of the session a call reads: the one
// named, else the page's.
func (c CallContext) transcriptTarget(named string) (agent, id string, err error) {
	if named == "" && c.HasPage {
		named = c.Page.Session
	}
	agent, id, ok := strings.Cut(named, ":")
	if !ok || agent == "" || id == "" {
		return "", "", errors.New("name a session as agent:id: no session page is open")
	}
	if err := segment("agent", agent); err != nil {
		return "", "", err
	}
	if err := segment("session", id); err != nil {
		return "", "", err
	}
	return agent, id, nil
}

func callLiveSessionTranscript(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Session  string `json:"session"`
		FromTurn int    `json:"from_turn"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	a.FromTurn = max(a.FromTurn, 1)
	agent, id, err := c.transcriptTarget(a.Session)
	if err != nil {
		return "", err
	}
	body, missing, err := c.get(ctx, "/api/live-sessions/"+url.PathEscape(agent)+"/"+url.PathEscape(id)+"/transcript", nil)
	if err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("session %s:%s is no longer listed", agent, id)
	}
	return transcriptText(body, a.FromTurn, boundRows(a.Limit, defaultPrompts, maxPrompts))
}

// transcriptText is a transcript answer as a tool returns it: the prompts
// from one turn, limit of them, without images or anything that names a
// place on the computer.
func transcriptText(body json.RawMessage, from, limit int) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var answer map[string]any
	if err := dec.Decode(&answer); err != nil {
		return "", errors.New("the transcript is not readable")
	}
	// The images' media types are the answer's own field; the images
	// themselves are never read.
	delete(answer, "images")
	if available, ok := answer["available"].(bool); ok && !available {
		return compactJSON(answer)
	}
	transcript, _ := answer["transcript"].(map[string]any)
	if session, ok := transcript["session"].(map[string]any); ok {
		// The session's working directory is a path on the computer, and the
		// person's address is theirs.
		delete(session, "working_dir")
		delete(session, "user_email")
		prompts, _ := session["prompts"].([]any)
		var kept []any
		next := 0
		for _, p := range prompts {
			ordinal := 0
			if prompt, ok := p.(map[string]any); ok {
				if n, ok := prompt["ordinal"].(json.Number); ok {
					v, _ := n.Int64()
					ordinal = int(v)
				}
			}
			if ordinal != 0 && ordinal < from {
				continue
			}
			if len(kept) == limit {
				next = ordinal
				break
			}
			kept = append(kept, p)
		}
		if kept == nil {
			kept = []any{}
		}
		session["prompts"] = kept
		if next > 0 {
			session["next_turn"] = next
		}
		// What the agent did (its messages and tool calls) is keyed by the
		// prompt's turn id: only the kept prompts' stays.
		if activity, ok := transcript["activity"].(map[string]any); ok {
			turns := map[string]bool{}
			for _, p := range kept {
				if prompt, ok := p.(map[string]any); ok {
					if id, ok := prompt["turn_id"].(string); ok {
						turns[id] = true
					}
				}
			}
			for id := range activity {
				if !turns[id] {
					delete(activity, id)
				}
			}
		}
	}
	scrub(answer, func(key string) bool { return strings.HasPrefix(key, "image") })
	return compactJSON(answer)
}

// scrub deletes, at any depth of the decoded JSON v, the keys drop names.
func scrub(v any, drop func(key string) bool) {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if drop(key) {
				delete(x, key)
				continue
			}
			scrub(value, drop)
		}
	case []any:
		for _, value := range x {
			scrub(value, drop)
		}
	}
}

// agentRequestsBody is the app's agent requests route, without its settings,
// which the person chooses and an agent has no need of.
func (c CallContext) agentRequestsBody(ctx context.Context) (requests, messages json.RawMessage, err error) {
	body, _, err := c.get(ctx, "/api/agent-requests", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("the agent requests are unavailable: %w", err)
	}
	if disabled(body) {
		return nil, nil, errors.New("the agent requests are unavailable: the daemon has none")
	}
	var parts struct {
		Requests json.RawMessage `json:"requests"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &parts); err != nil {
		return nil, nil, errors.New("the agent requests are not readable")
	}
	return orEmpty(parts.Requests), orEmpty(parts.Messages), nil
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

func callAgentRequests(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	requests, _, err := c.agentRequestsBody(ctx)
	if err != nil {
		return "", err
	}
	return compactJSON(struct {
		Requests json.RawMessage `json:"requests"`
	}{requests})
}

func callAgentMessages(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	_, messages, err := c.agentRequestsBody(ctx)
	if err != nil {
		return "", err
	}
	return compactJSON(struct {
		Messages json.RawMessage `json:"messages"`
	}{messages})
}
