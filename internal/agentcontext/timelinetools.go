package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The tools of the Timeline and a feature's page. A decision, feature or
// file belongs to one repository, so the tools that read them are
// repository-scoped, and each route they read is bound to the action's
// repository in actionRoutes. A session's transcript names no repository, so
// timeline_transcript is global.

const (
	defaultTimelineRows = 25
	maxTimelineRows     = 100
	maxFileRows         = 50
)

func (s *Server) timelineTools() []Tool {
	repository := `"repository":{"type":"string","description":"Only this repository, as owner/name. Default: the page's repo filter, else every repository with decisions on. An action's is always its own."}`
	paging := `"limit":{"type":"integer","minimum":1,"maximum":100,"description":"The most to return. Default 25."},` +
		`"cursor":{"type":"string","description":"The next_cursor a result ended with, for the next page."}`
	day := func(name string) string {
		return `"` + name + `":{"type":"string","maxLength":40,"description":"A day as 2026-10-31, or a time as RFC 3339. Default: the page's."}`
	}
	return []Tool{{
		Name: "decisions",
		Description: "The Timeline's All decisions list: the decisions agents and people made in captured sessions, newest first, each with who made it (human, agent_proposed_human_approved, agent), its category, summary, session, date and files. " +
			"Narrow it as the page's filters do (who, repository, file, category, person, dates, words); the page's own filters apply unless given.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `,` +
			`"who":{"type":"array","maxItems":3,"items":{"type":"string","enum":["human","agent_proposed_human_approved","agent"]},"description":"Only decisions made by these. Default: the page's."},` +
			`"category":{"type":"array","maxItems":9,"items":{"type":"string","enum":["architecture","data_model","interface","behavior","implementation","dependency","testing","scope","process"]},"description":"Only these categories."},` +
			`"file":{"type":"string","maxLength":500,"description":"Only decisions citing this file or folder."},` +
			`"person":{"type":"string","maxLength":200,"description":"Only this person's, by email."},` +
			`"q":{"type":"string","maxLength":200,"description":"Words to look for in the decisions."},` +
			`"superseded":{"type":"boolean","description":"Include decisions a later one replaced."},` +
			day("from") + `,` + day("to") + `,` + paging + `},"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/timeline/decisions"},
		Call:  callDecisions,
	}, {
		Name:        "decision",
		Description: "One decision as the Timeline's decision page shows it: its session's current decisions with their files and rejected alternatives, the prompts they cite, and the pull requests the session's commits are in.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"The decision's id, from decisions."}},"required":["id"],"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/timeline/decisions"},
		Call:        callDecision,
	}, {
		Name: "features",
		Description: "The Timeline's Features list: a repository's features of its current version, newest first, each with its title, status (in_progress, shipped or abandoned), people, pull request, session and decision counts and its key decisions. " +
			"The page's own filters apply unless given.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `,` +
			`"status":{"type":"array","maxItems":3,"items":{"type":"string","enum":["in_progress","shipped","abandoned"]},"description":"Only features with these statuses. Default: the page's."},` +
			`"file":{"type":"string","maxLength":500,"description":"Only features touching this file or folder."},` +
			`"person":{"type":"string","maxLength":200,"description":"Only features this person is among the people of, by email."},` +
			`"q":{"type":"string","maxLength":200,"description":"Words to look for in the features."},` +
			day("from") + `,` + day("to") + `,` + paging + `},"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/timeline/features"},
		Call:  callFeatures,
	}, {
		Name:        "feature",
		Description: "One feature as its page shows it: the title, description and digest, its pull requests with their decisions and its sessions with theirs. Defaults to the open feature page's.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"The feature's id. Default: the page's feature."}},"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/timeline/features"},
		Call:        callFeature,
	}, {
		Name:        "feature_build",
		Description: "Whether a repository's features are being built for the first time, how far (pull requests placed of the total, what was spent), why a build stopped, whether it has features and since when its current version has been current.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","description":"The repository as owner/name. Default: the page's, or for an action its own."}},"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/timeline/build"},
		Call:        callFeatureBuild,
	}, {
		Name: "file_history",
		Description: "The files and folders the repository's current decisions cite that start with a prefix, with how many decisions cite each, as the Timeline's file filter offers them: most cited first, folders before files. " +
			"Read decisions with file set to one of them for the decisions themselves.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `,` +
			`"prefix":{"type":"string","maxLength":500,"description":"The path they start with. Default: all."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":50,"description":"The most to return. Default 20."}},"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/timeline/files"},
		Call:  callFileHistory,
	}, {
		Name: "timeline_transcript",
		Description: "A captured session's transcript as the Timeline shows it, whether or not the session is still running: its prompts in order with the agent's replies, effects and cost. Images pasted with a prompt are never returned. " +
			"The prompts come limit at a time (default 20, at most 100) from turn from_turn; the result names the next turn.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"session":{"type":"string","description":"The session as agent:id, as a decision names it (session_id). Default: the page's session."},` +
			`"from_turn":{"type":"integer","minimum":1,"description":"The first prompt's ordinal to return. Default 1."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":100,"description":"The most prompts to return. Default 20."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/timeline/sessions"},
		Call:  callTimelineTranscript,
	}}
}

// timelineRepositories are the repositories a Timeline call reads: the one
// named, an action's own, else the page's repo filter, or its repository on a
// page that has one; none means every repository with decisions on. The name
// is checked by Target, which refuses an action any other repository.
func (c CallContext) timelineRepositories(named string) ([]string, error) {
	if c.Consumer.Kind == Action || named != "" {
		repository, _, err := c.Target(named, 0)
		if err != nil {
			return nil, err
		}
		return []string{repository}, nil
	}
	var out []string
	for part := range strings.SplitSeq(c.pageFilter("repo", PageTimeline, PageFeature), ",") {
		if part = strings.TrimSpace(part); part != "" {
			repository, _, err := c.Target(part, 0)
			if err != nil {
				return nil, err
			}
			out = append(out, repository)
		}
	}
	if len(out) == 0 && c.HasPage && c.Page.Page == PageFeature && c.Page.Repository != "" {
		repository, _, err := c.Target("", 0)
		if err != nil {
			return nil, err
		}
		out = append(out, repository)
	}
	return out, nil
}

// timelineQuery is the query of a Timeline list: the repositories, the
// filters the call gave or else the page's own, and the page asked for.
func (c CallContext) timelineQuery(args json.RawMessage, repository string, listed ...string) (url.Values, error) {
	var a map[string]json.RawMessage
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, err
	}
	repositories, err := c.timelineRepositories(repository)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	for _, r := range repositories {
		query.Add("repository", r)
	}
	for _, name := range listed {
		value := ""
		if raw, ok := a[name]; ok {
			var list []string
			var text string
			var flag bool
			switch {
			case json.Unmarshal(raw, &list) == nil:
				value = strings.Join(list, ",")
			case json.Unmarshal(raw, &text) == nil:
				value = text
			case json.Unmarshal(raw, &flag) == nil && flag:
				value = "1"
			}
		} else if name != "limit" && name != "cursor" {
			// The page's own filter, in the address's words.
			value = c.pageFilter(name, PageTimeline, PageFeature)
			if name == "superseded" && value != "1" {
				value = ""
			}
		}
		if value != "" {
			query.Set(name, value)
		}
	}
	if raw, ok := a["limit"]; ok {
		var n int
		_ = json.Unmarshal(raw, &n)
		query.Set("limit", strconv.Itoa(boundRows(n, defaultTimelineRows, maxTimelineRows)))
	} else {
		query.Set("limit", strconv.Itoa(defaultTimelineRows))
	}
	if query.Get("who") == "" && c.pageFilter("agent", PageTimeline, PageFeature) == "hide" && containsName(listed, "who") {
		query.Set("who", "human,agent_proposed_human_approved")
	}
	return query, nil
}

func containsName(list []string, name string) bool {
	for _, one := range list {
		if one == name {
			return true
		}
	}
	return false
}

// readTimeline reads a Timeline route and returns its JSON without what a
// person's avatar and address are: the app draws initials from the name.
func (c CallContext) readTimeline(ctx context.Context, path string, query url.Values) (string, error) {
	body, missing, err := c.get(ctx, path, query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("not found: it does not exist, or its repository has decisions turned off")
	}
	if disabled(body) {
		return "", errors.New("the Timeline is unavailable: the daemon has none")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", errors.New("the Timeline answer is not readable")
	}
	scrub(v, func(key string) bool { return key == "avatar_url" || key == "email" })
	return compactJSON(v)
}

func callDecisions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	query, err := c.timelineQuery(args, a.Repository, "q", "who", "category", "file", "person", "superseded", "from", "to", "limit", "cursor")
	if err != nil {
		return "", err
	}
	return c.readTimeline(ctx, "/api/timeline/decisions", query)
}

func callFeatures(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	query, err := c.timelineQuery(args, a.Repository, "q", "status", "file", "person", "from", "to", "limit", "cursor")
	if err != nil {
		return "", err
	}
	return c.readTimeline(ctx, "/api/timeline/features", query)
}

func callDecision(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if err := segment("decision", a.ID); err != nil {
		return "", err
	}
	return c.readTimeline(ctx, "/api/timeline/decisions/"+url.PathEscape(a.ID), nil)
}

func callFeature(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.ID == "" && c.HasPage {
		a.ID = c.Page.Feature
	}
	if a.ID == "" {
		return "", errors.New("name a feature: no feature page is open")
	}
	if err := segment("feature", a.ID); err != nil {
		return "", err
	}
	return c.readTimeline(ctx, "/api/timeline/features/"+url.PathEscape(a.ID), nil)
}

func callFeatureBuild(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", err
	}
	return c.readTimeline(ctx, "/api/timeline/build", url.Values{"repository": {repository}})
}

func callFileHistory(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
		Prefix     string `json:"prefix"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repositories, err := c.timelineRepositories(a.Repository)
	if err != nil {
		return "", err
	}
	query := url.Values{"limit": {strconv.Itoa(boundRows(a.Limit, 20, maxFileRows))}}
	for _, r := range repositories {
		query.Add("repository", r)
	}
	if a.Prefix != "" {
		query.Set("prefix", a.Prefix)
	}
	return c.readTimeline(ctx, "/api/timeline/files", query)
}

func callTimelineTranscript(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
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
	// The Timeline names a session agent:id, one path segment.
	body, missing, err := c.get(ctx, "/api/timeline/sessions/"+url.PathEscape(agent+":"+id)+"/transcript", nil)
	if err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("session %s:%s has no captured transcript", agent, id)
	}
	return transcriptText(body, a.FromTurn, boundRows(a.Limit, defaultPrompts, maxPrompts))
}
