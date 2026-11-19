package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/reviewv2"
)

// The tools a review's agent reads most beyond the page's own: a pull
// request's history on GitHub (timeline, every review thread, issues), the
// clone's git history (log, blame, any file), what changed between two saved
// versions of its review, its related pull requests, and the checks its
// sessions ran. Every one only reads, and chats and actions have them too.

// reviewReadTools are the tools of this file.
func (s *Server) reviewReadTools() []Tool {
	index := "/api/index"
	return []Tool{{
		Name: "transcript_search",
		Description: "Searches the redacted transcript of one session linked to the pull request (see sessions) for words: the turns whose prompt, replies, agent messages or tool calls hold every word, with the matching text around each hit and the turn's prompt. " +
			"Read a turn whole with session_transcript from_turn.",
		InputSchema: targetSchema(`"session":{"type":"string","description":"The session's id, from sessions."},`+
			`"query":{"type":"string","minLength":1,"maxLength":200,"description":"Words to find, case-insensitively; a turn matches when it holds all of them."},`+
			`"limit":{"type":"integer","minimum":1,"maximum":50,"description":"The most turns to return. Default 10."}`, "session", "query"),
		Scope: ScopeRepository,
		Reads: []string{index, "/api/facts", "/api/sessions"},
		Call:  callTranscriptSearch,
	}, {
		Name:        "pull_request_timeline",
		Description: "The pull request's history on GitHub, oldest first: commits pushed and force-pushes (with the heads before and after where GitHub gives them), base changes, review submissions with their state and text, comments, labels, assignments, review requests, ready for review and back to draft, merge, close and reopen, and the issues and pull requests that mention it. Text is the authors' own, untrusted.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/prs/timeline"},
		Call:        callPullTimeline,
	}, {
		Name: "issue",
		Description: "A GitHub issue of the repository by number: title, state, author, body, labels, comments and the pull requests linked to it. References in a pull request's description or commits (#123) may name one; a number that is a pull request is not found here (read it with pull_request). " +
			"Only GitHub issues: baum has no Linear or other tracker integration.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"repository":{"type":"string","description":"The repository as owner/name. Defaults to the page's, or the review's or action's own."},` +
			`"number":{"type":"integer","minimum":1,"description":"The issue number."}},"required":["number"],"additionalProperties":false}`),
		Scope: ScopeRepository,
		Reads: []string{"/api/issue"},
		Call:  callIssue,
	}, {
		Name:        "git_log",
		Description: "The commits of the pull request's clone that touched a path (or the whole history), newest first, from its head (default), its merge base with the base branch (rev base) or any commit id: id, parents, author, date, subject, and the pull request each came from where baum knows it (a merge or squash subject naming #N, or a pull request's head). Never fetches: a commit the clone lacks is reported.",
		InputSchema: targetSchema(gitRevProperty + `,"path":{"type":"string","description":"A repository path. Omit for the whole history."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most commits. Default 30."},` +
			`"since":{"type":"string","description":"Only commits since this date (YYYY-MM-DD)."}`),
		Scope: ScopeRepository,
		Reads: []string{index, "/api/document", "/api/facts", "/api/clone/log"},
		Call:  callGitLog,
	}, {
		Name:        "git_blame",
		Description: "Who last changed each line of a file's range, at the pull request's head (default), its merge base (rev base) or any commit id: line, commit, author, date, the commit's subject and the pull request it came from where baum knows it, and the line's text. At most 2000 lines.",
		InputSchema: targetSchema(gitRevProperty+`,"path":{"type":"string","description":"A repository path."},"start":{"type":"integer","minimum":1,"description":"The first line."},"end":{"type":"integer","minimum":1,"description":"The last line. Default start + 99."}`, "path", "start"),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/document", "/api/facts", "/api/clone/blame"},
		Call:        callGitBlame,
	}, {
		Name:        "review_version_diff",
		Description: "What changed between two saved versions of the pull request's review: heads, sections added, removed and renamed, hunks that moved between sections, risks added, mitigated and removed, and changes to the recommendation and the risk score. Defaults: to the latest version, from the one before.",
		InputSchema: targetSchema(`"from":{"type":"integer","minimum":1,"description":"The earlier version. Default: the one before to."},` +
			`"to":{"type":"integer","minimum":1,"description":"The later version. Default: the latest."}`),
		Scope: ScopeRepository,
		Reads: []string{index, "/api/versions", "/api/document"},
		Call:  callReviewVersionDiff,
	}, {
		Name: "related_pull_requests",
		Description: "Pull requests related to this one, each with number, title, state, author, branches, when merged where known and whether baum has a saved review of it: its stack (the pull request whose head branch it is based on, those based on its head branch, and its stack groups), the issues and pull requests that mention it on GitHub, and open and recently merged pull requests that touch the same files (its changed files by default, or the paths given). " +
			"Open ones are compared in the clone, so a pull request whose head the clone lacks is skipped; merged ones are found from the base branch's history.",
		InputSchema: targetSchema(`"paths":{"type":"array","maxItems":50,"items":{"type":"string","minLength":1},"description":"Paths to compare. Default: the pull request's changed files."},` +
			`"state":{"type":"string","enum":["open","merged","all"],"description":"Which pull requests touching the same files. Default all."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":50,"description":"The most pull requests of each kind. Default 20."}`),
		Scope: ScopeRepository,
		Reads: []string{index, "/api/groups", "/api/document", "/api/facts", "/api/prs/timeline", "/api/clone/changed", "/api/clone/log"},
		Call:  callRelatedPullRequests,
	}, {
		Name:        "validation_runs",
		Description: "The checks the pull request's linked sessions ran, as baum extracted them from their transcripts: each check's method (the command or kind), what it checked, its outcome, its evidence class and caveats, and whether it ran on the reviewed head, an earlier commit or uncommitted work; per session, what was not verified and what was set aside and why, and why extraction was unavailable. From the latest saved review, else the facts.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/document", "/api/facts"},
		Call:        callValidationRuns,
	}}
}

const gitRevProperty = `"rev":{"type":"string","description":"head (default), base (the merge base with the base branch) or a commit id."}`

// sessionTranscriptSchema is session_transcript's: the session, and paging by
// turn.
var sessionTranscriptSchema = targetSchema(`"id":{"type":"string","description":"The session's id, from sessions."},`+
	`"from_turn":{"type":"integer","minimum":1,"description":"The first prompt's ordinal to return. With from_turn or limit the prompts come limit at a time and the result names the next turn; without either, the whole transcript."},`+
	`"limit":{"type":"integer","minimum":1,"maximum":100,"description":"The most prompts to return. Default 20."}`, "id")

// callSessionTranscriptPaged is session_transcript: the whole transcript, as
// before, or with from_turn or limit the prompts from one turn on.
func callSessionTranscriptPaged(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		FromTurn int `json:"from_turn"`
		Limit    int `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.FromTurn == 0 && a.Limit == 0 {
		return callSessionTranscript(ctx, c, args)
	}
	body, err := sessionTranscriptBody(ctx, c, args)
	if err != nil {
		return "", err
	}
	return transcriptText(json.RawMessage(`{"transcript":`+string(body)+`}`), max(a.FromTurn, 1), boundRows(a.Limit, defaultPrompts, maxPrompts))
}

// sessionTranscriptBody reads a linked session's transcript, named by id or
// session.
func sessionTranscriptBody(ctx context.Context, c CallContext, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		ID      string `json:"id"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, err
	}
	id := a.ID
	if id == "" {
		id = a.Session
	}
	if err := segment("session", id); err != nil {
		return nil, err
	}
	return readSource(ctx, c, args, "sessions/"+url.PathEscape(id))
}

// transcriptBody is what transcript_search reads of a transcript.
type transcriptBody struct {
	Session struct {
		Prompts []struct {
			TurnID    string `json:"turn_id"`
			Ordinal   int    `json:"ordinal"`
			StartedAt string `json:"started_at"`
			Text      string `json:"text"`
			Replies   []struct {
				Text string `json:"text"`
			} `json:"replies"`
		} `json:"prompts"`
	} `json:"session"`
	Activity map[string]struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
		ToolCalls []struct {
			Name   string  `json:"name"`
			Input  string  `json:"input"`
			Output *string `json:"output"`
		} `json:"tool_calls"`
	} `json:"activity"`
	Truncated bool `json:"truncated"`
}

// searchHit is one place a turn matched.
type searchHit struct {
	Where   string `json:"where"`
	Snippet string `json:"snippet"`
}

// snippetRadius is how much text a hit keeps on each side of the first word.
const snippetRadius = 200

func callTranscriptSearch(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Session string `json:"session"`
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	words := strings.Fields(strings.ToLower(a.Query))
	if len(words) == 0 {
		return "", errors.New("name words to find")
	}
	body, err := sessionTranscriptBody(ctx, c, args)
	if err != nil {
		return "", err
	}
	var t transcriptBody
	if err := json.Unmarshal(body, &t); err != nil {
		return "", errors.New("the transcript is not readable")
	}
	limit := boundRows(a.Limit, 10, 50)
	type turn struct {
		Turn      int         `json:"turn"`
		StartedAt string      `json:"started_at,omitempty"`
		Prompt    string      `json:"prompt"`
		Hits      []searchHit `json:"hits"`
	}
	turns := []turn{}
	more := false
	for _, p := range t.Session.Prompts {
		var texts []searchHit
		texts = append(texts, searchHit{"prompt", p.Text})
		for _, r := range p.Replies {
			texts = append(texts, searchHit{"reply", r.Text})
		}
		activity := t.Activity[p.TurnID]
		for _, m := range activity.Messages {
			texts = append(texts, searchHit{"agent message", m.Text})
		}
		for _, call := range activity.ToolCalls {
			texts = append(texts, searchHit{"tool call " + call.Name + " input", call.Input})
			if call.Output != nil {
				texts = append(texts, searchHit{"tool call " + call.Name + " output", *call.Output})
			}
		}
		// A turn matches when its texts together hold every word.
		all := strings.Builder{}
		for _, text := range texts {
			all.WriteString(strings.ToLower(text.Snippet))
			all.WriteByte('\n')
		}
		joined := all.String()
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(joined, w) }) {
			var hits []searchHit
			for _, text := range texts {
				if snippet, ok := snippetOf(text.Snippet, words); ok {
					hits = append(hits, searchHit{text.Where, snippet})
				}
			}
			if len(turns) == limit {
				more = true
				break
			}
			turns = append(turns, turn{Turn: p.Ordinal, StartedAt: p.StartedAt, Prompt: cutText(p.Text, 300), Hits: hits})
		}
	}
	out := map[string]any{"query": a.Query, "turns": turns, "more": more}
	if t.Truncated {
		out["note"] = "the transcript baum keeps is cut to its size limit: parts of it are not searched"
	}
	return compactJSON(out)
}

// snippetOf is text around the first of words text holds, found without
// regard to case. The text is lowered one character at a time, so the
// offsets of the match are the original's.
func snippetOf(text string, words []string) (string, bool) {
	at := -1
	for _, w := range words {
		if i := indexFold(text, w); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	at = min(at, len(text))
	start, end := max(at-snippetRadius, 0), min(at+snippetRadius, len(text))
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	snippet := text[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet, true
}

// indexFold is the byte offset in text of the first match of word, a
// lowercase string, comparing each character of text lowered; -1 when there
// is none. Unlike strings.Index over strings.ToLower(text), the offset is
// into text itself: lowering can change a character's byte length.
func indexFold(text, word string) int {
	if word == "" {
		return 0
	}
	for i := 0; i < len(text); {
		if !utf8.RuneStart(text[i]) {
			i++
			continue
		}
		if matchFold(text[i:], word) {
			return i
		}
		_, n := utf8.DecodeRuneInString(text[i:])
		i += n
	}
	return -1
}

// matchFold reports whether text begins with word, text's characters lowered.
func matchFold(text, word string) bool {
	for _, w := range word {
		if text == "" {
			return false
		}
		r, n := utf8.DecodeRuneInString(text)
		if unicode.ToLower(r) != w {
			return false
		}
		text = text[n:]
	}
	return true
}

// cutText is text at most n bytes, on a character boundary.
func cutText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "…"
}

// reviewThreadsSchema is review_threads': the pull request and which threads.
var reviewThreadsSchema = targetSchema(`"state":{"type":"string","enum":["unresolved","resolved","all"],"description":"Which threads. Default unresolved."}`)

// callReviewThreadsState is review_threads: the unresolved threads as the
// app lists them, or with state the resolved ones or all.
func callReviewThreadsState(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.State == "" || a.State == "unresolved" {
		return callReviewThreads(ctx, c, args)
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	query := pullQuery(repository, number)
	query.Set("state", a.State)
	body, missing, err := c.get(ctx, "/api/prs/threads", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the review threads are not available")
	}
	return string(body), nil
}

func callPullTimeline(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	body, missing, err := c.get(ctx, "/api/prs/timeline", pullQuery(repository, number))
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the pull request's timeline is not available")
	}
	return string(body), nil
}

func callIssue(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Repository string `json:"repository"`
		Number     int    `json:"number"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", err
	}
	body, missing, err := c.get(ctx, "/api/issue", url.Values{"repository": {repository}, "number": {strconv.Itoa(a.Number)}})
	if err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("%s has no issue #%d: it may be a pull request", repository, a.Number)
	}
	return string(body), nil
}

// docHeader is the "review" object of a review or facts document.
type docHeader struct {
	Head      string `json:"head"`
	Base      string `json:"base"`
	MergeBase string `json:"merge_base"`
	Version   int    `json:"version"`
}

// pullDocument reads the pull request's latest saved review, or its facts
// when it has none, as the page does.
func (c CallContext) pullDocument(ctx context.Context, e reviewEntry) (json.RawMessage, string, error) {
	query, err := e.selector()
	if err != nil {
		return nil, "", err
	}
	path, name := "/api/facts", "facts"
	if e.VersionCount > 0 {
		path, name = "/api/document", "review"
	} else if e.HeadSHA != "" {
		query.Set("head", e.HeadSHA)
	}
	body, missing, err := c.get(ctx, path, query)
	if err != nil {
		return nil, "", err
	}
	if missing {
		return nil, "", fmt.Errorf("the pull request's %s are not available", name)
	}
	return body, name, nil
}

// clonePull is a pull request with its clone, for the git tools.
type clonePull struct {
	entry      reviewEntry
	repository string
	number     int
	query      url.Values
	header     *docHeader
}

func (c CallContext) clonePull(ctx context.Context, args json.RawMessage) (*clonePull, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return nil, err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return nil, err
	}
	query, err := e.selector()
	if err != nil {
		return nil, err
	}
	query.Del("pr")
	return &clonePull{entry: e, repository: repository, number: number, query: query}, nil
}

// reviewHeader reads the header of the pull request's latest review, or of
// its facts: the heads it describes.
func (p *clonePull) reviewHeader(ctx context.Context, c CallContext) (docHeader, error) {
	if p.header != nil {
		return *p.header, nil
	}
	body, _, err := c.pullDocument(ctx, p.entry)
	if err != nil {
		return docHeader{}, err
	}
	var doc struct {
		Review docHeader `json:"review"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return docHeader{}, errors.New("the pull request's review is not readable")
	}
	p.header = &doc.Review
	return doc.Review, nil
}

var commitIDPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// revisions are the commits a git tool reads at for rev, in order: the
// pull request's head is the one discovery shows, then the one its review or
// facts describe.
func (p *clonePull) revisions(ctx context.Context, c CallContext, rev string) ([]string, error) {
	switch rev = strings.TrimSpace(rev); rev {
	case "", "head":
		var out []string
		if p.entry.HeadSHA != "" {
			out = append(out, p.entry.HeadSHA)
		}
		if h, err := p.reviewHeader(ctx, c); err == nil && h.Head != "" && h.Head != p.entry.HeadSHA {
			out = append(out, h.Head)
		}
		if len(out) == 0 {
			return nil, errors.New("the pull request's head is not known")
		}
		return out, nil
	case "base":
		h, err := p.reviewHeader(ctx, c)
		if err != nil {
			return nil, err
		}
		if h.MergeBase != "" {
			return []string{h.MergeBase}, nil
		}
		if h.Base != "" {
			return []string{h.Base}, nil
		}
		return nil, errors.New("the pull request's base is not known")
	}
	if !commitIDPattern.MatchString(rev) {
		return nil, errors.New("rev is head, base or a commit id")
	}
	return []string{rev}, nil
}

// readAtRevision reads route at each of revisions until the clone has one.
func (p *clonePull) readAtRevision(ctx context.Context, c CallContext, route string, revisions []string, query url.Values) (json.RawMessage, string, error) {
	var last error
	for _, rev := range revisions {
		q := url.Values{}
		for k, v := range p.query {
			q[k] = v
		}
		for k, v := range query {
			q[k] = v
		}
		q.Set("rev", rev)
		body, missing, err := c.get(ctx, route, q)
		switch {
		case err == nil && !missing:
			return body, rev, nil
		case err == nil:
			// Not found is a clone the daemon no longer has.
			return nil, "", errors.New("the pull request's clone is not available")
		case !strings.Contains(err.Error(), "commit_missing"):
			return nil, "", err
		}
		last = err
	}
	return nil, "", fmt.Errorf("the clone does not have the commit (it was never fetched): %w", last)
}

// pullLinks maps commits to the pull requests they came from: a merge or
// squash subject that names one, else a pull request whose head it is.
type pullLinks struct {
	heads map[string]int
}

var (
	squashSubject = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	mergeSubject  = regexp.MustCompile(`^Merge pull request #(\d+)\b`)
)

func (c CallContext) pullLinks(ctx context.Context, repository string) pullLinks {
	links := pullLinks{heads: map[string]int{}}
	for _, e := range c.indexPulls(ctx, repository) {
		if e.HeadSHA != "" {
			links.heads[e.HeadSHA] = e.Number
		}
	}
	return links
}

// of is the pull request a commit came from, and how that is known.
func (l pullLinks) of(sha, subject string) (int, string) {
	if m := mergeSubject.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, "merge subject"
	}
	if m := squashSubject.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, "subject"
	}
	if n, ok := l.heads[sha]; ok {
		return n, "pull request head"
	}
	return 0, ""
}

// indexPull is what the related tools read of an index entry.
type indexPull struct {
	Number       int
	HeadSHA      string
	Title        string
	State        string
	Author       string
	Draft        bool
	HeadRef      string
	BaseRef      string
	URL          string
	VersionCount int
	Historical   bool
}

// indexPulls are the repository's entries in the review index, with
// history, one per pull request (the one with a saved review first).
func (c CallContext) indexPulls(ctx context.Context, repository string) []indexPull {
	body, _, err := c.get(ctx, "/api/index", url.Values{"history": {"1"}})
	if err != nil {
		return nil
	}
	var index struct {
		Entries []struct {
			PullRequest struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
				HeadSHA    string `json:"head_sha"`
				Title      string `json:"title"`
				State      string `json:"state"`
				Author     string `json:"author"`
				Draft      bool   `json:"draft"`
				HeadRef    string `json:"head_ref"`
				BaseRef    string `json:"base_ref"`
				URL        string `json:"url"`
			} `json:"pull_request"`
			VersionCount int  `json:"version_count"`
			Historical   bool `json:"historical"`
		} `json:"entries"`
	}
	if json.Unmarshal(body, &index) != nil {
		return nil
	}
	byNumber := map[int]int{}
	var out []indexPull
	for _, e := range index.Entries {
		pr := e.PullRequest
		if !strings.EqualFold(pr.Repository, repository) {
			continue
		}
		p := indexPull{Number: pr.Number, HeadSHA: pr.HeadSHA, Title: pr.Title, State: strings.ToLower(pr.State), Author: pr.Author, Draft: pr.Draft,
			HeadRef: pr.HeadRef, BaseRef: pr.BaseRef, URL: pr.URL, VersionCount: e.VersionCount, Historical: e.Historical}
		if i, ok := byNumber[pr.Number]; ok {
			if p.VersionCount > out[i].VersionCount {
				out[i] = p
			}
			continue
		}
		byNumber[pr.Number] = len(out)
		out = append(out, p)
	}
	return out
}

func callGitLog(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Rev   string `json:"rev"`
		Path  string `json:"path"`
		Limit int    `json:"limit"`
		Since string `json:"since"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, err := c.clonePull(ctx, args)
	if err != nil {
		return "", err
	}
	revisions, err := p.revisions(ctx, c, a.Rev)
	if err != nil {
		return "", err
	}
	query := url.Values{"limit": {strconv.Itoa(boundRows(a.Limit, 30, 200))}}
	if a.Path != "" {
		query.Set("path", a.Path)
	}
	if a.Since != "" {
		query.Set("since", a.Since)
	}
	body, rev, err := p.readAtRevision(ctx, c, "/api/clone/log", revisions, query)
	if err != nil {
		return "", err
	}
	var log map[string]any
	if err := json.Unmarshal(body, &log); err != nil {
		return "", errors.New("the log is not readable")
	}
	links := c.pullLinks(ctx, p.repository)
	if commits, ok := log["commits"].([]any); ok {
		for _, raw := range commits {
			if commit, ok := raw.(map[string]any); ok {
				sha, _ := commit["sha"].(string)
				subject, _ := commit["subject"].(string)
				if n, how := links.of(sha, subject); n > 0 {
					commit["pull_request"], commit["pull_request_from"] = n, how
				}
			}
		}
	}
	log["rev_asked"] = cmpOr(a.Rev, "head")
	log["rev"] = rev
	return compactJSON(log)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func callGitBlame(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Rev   string `json:"rev"`
		Path  string `json:"path"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.End == 0 {
		a.End = a.Start + 99
	}
	if a.End < a.Start || a.End-a.Start >= 2000 {
		return "", errors.New("end must be at least start, at most 2000 lines on")
	}
	p, err := c.clonePull(ctx, args)
	if err != nil {
		return "", err
	}
	revisions, err := p.revisions(ctx, c, a.Rev)
	if err != nil {
		return "", err
	}
	body, rev, err := p.readAtRevision(ctx, c, "/api/clone/blame", revisions,
		url.Values{"path": {a.Path}, "start": {strconv.Itoa(a.Start)}, "end": {strconv.Itoa(a.End)}})
	if err != nil {
		return "", err
	}
	var blame map[string]any
	if err := json.Unmarshal(body, &blame); err != nil {
		return "", errors.New("the blame is not readable")
	}
	links := c.pullLinks(ctx, p.repository)
	if lines, ok := blame["lines"].([]any); ok {
		for _, raw := range lines {
			if line, ok := raw.(map[string]any); ok {
				sha, _ := line["sha"].(string)
				summary, _ := line["summary"].(string)
				if n, _ := links.of(sha, summary); n > 0 {
					line["pull_request"] = n
				}
			}
		}
	}
	blame["rev_asked"] = cmpOr(a.Rev, "head")
	blame["rev"] = rev
	return compactJSON(blame)
}

// fileSchema is file's: a path, and the commit to read it at.
var fileSchema = targetSchema(`"path":{"type":"string","description":"A repository path."},`+gitRevProperty, "path")

// callFileAnyRev is file: a file the saved review cites, as the page shows
// it, else any file of the clone at the pull request's head, its base or a
// commit.
func callFileAnyRev(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
		Rev  string `json:"rev"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", errors.New("name a path")
	}
	if a.Rev == "" {
		if text, err := callFile(ctx, c, args); err == nil {
			return text, nil
		}
	}
	p, err := c.clonePull(ctx, args)
	if err != nil {
		return "", err
	}
	revisions, err := p.revisions(ctx, c, a.Rev)
	if err != nil {
		return "", err
	}
	body, _, err := p.readAtRevision(ctx, c, "/api/clone/file", revisions, url.Values{"path": {a.Path}})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// versionShape is what review_version_diff compares of a review document:
// either schema read as baum.review/2 (readReview), with the schema it was
// stored in.
type versionShape struct {
	Schema string
	Header reviewHeader
	Doc    reviewv2.Document
}

func readVersionShape(body []byte) (versionShape, error) {
	doc, header, err := readReview(body)
	if err != nil {
		return versionShape{}, err
	}
	var head struct {
		Schema string `json:"schema"`
	}
	_ = json.Unmarshal(body, &head)
	return versionShape{Schema: head.Schema, Header: header, Doc: doc}, nil
}

func callReviewVersionDiff(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	if e.VersionCount == 0 {
		return "", errors.New("the pull request has no saved review")
	}
	query, err := e.selector()
	if err != nil {
		return "", err
	}
	read := func(version int) (versionShape, error) {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		if version > 0 {
			q.Set("version", strconv.Itoa(version))
		}
		body, missing, err := c.get(ctx, "/api/document", q)
		if err != nil {
			return versionShape{}, err
		}
		if missing {
			return versionShape{}, fmt.Errorf("version %d is not saved", version)
		}
		return readVersionShape(body)
	}
	to, err := read(a.To)
	if err != nil {
		return "", err
	}
	if a.From == 0 {
		a.From = to.Header.Version - 1
	}
	if a.From < 1 || a.From >= to.Header.Version {
		return "", fmt.Errorf("from must be a saved version before %d", to.Header.Version)
	}
	from, err := read(a.From)
	if err != nil {
		return "", err
	}
	return compactJSON(diffVersions(from, to))
}

// versionDiff is what changed from one saved review version to another.
type versionDiff struct {
	Versions          [2]int            `json:"versions"`
	Schemas           []string          `json:"schemas,omitempty"`
	Heads             map[string]string `json:"heads"`
	SectionsAdded     []sectionRef      `json:"sections_added"`
	SectionsRemoved   []sectionRef      `json:"sections_removed"`
	SectionsRenamed   []sectionRename   `json:"sections_renamed"`
	HunksMoved        []hunkMove        `json:"hunks_moved"`
	HunksAdded        int               `json:"hunks_added"`
	HunksRemoved      int               `json:"hunks_removed"`
	RisksAdded        []riskRef         `json:"risks_added"`
	RisksMitigated    []riskRef         `json:"risks_mitigated"`
	RisksRemoved      []riskRef         `json:"risks_removed"`
	Recommendation    map[string]any    `json:"recommendation,omitempty"`
	RiskScore         map[string]any    `json:"risk_score,omitempty"`
	SectionsUnchanged int               `json:"sections_unchanged"`
}

type sectionRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type sectionRename struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

type hunkMove struct {
	Hunk string `json:"hunk"`
	Path string `json:"path,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
}

type riskRef struct {
	Section string `json:"section"`
	ID      string `json:"id"`
	Tier    string `json:"tier,omitempty"`
	Text    string `json:"text"`
}

func diffVersions(from, to versionShape) versionDiff {
	d := versionDiff{Versions: [2]int{from.Header.Version, to.Header.Version},
		Heads:         map[string]string{"from": from.Header.Head, "to": to.Header.Head},
		SectionsAdded: []sectionRef{}, SectionsRemoved: []sectionRef{}, SectionsRenamed: []sectionRename{}, HunksMoved: []hunkMove{},
		RisksAdded: []riskRef{}, RisksMitigated: []riskRef{}, RisksRemoved: []riskRef{}}
	if from.Schema != to.Schema {
		d.Schemas = []string{from.Schema, to.Schema}
	}
	oldSections := map[string]string{}
	oldHunks := map[string]string{}
	type riskAt struct {
		section string
		state   string
	}
	oldRisks := map[string]riskAt{}
	for _, s := range from.Doc.Sections {
		oldSections[s.ID] = s.Title
		for _, h := range s.Hunks {
			oldHunks[h] = s.ID
		}
		for _, r := range s.Risks {
			oldRisks[r.ID] = riskAt{s.ID, r.State}
		}
	}
	paths := map[string]string{}
	var index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(to.Doc.Index, &index)
	for _, h := range index.Hunks {
		paths[h.ID] = h.Path
	}
	newSections := map[string]bool{}
	newHunks := map[string]bool{}
	newRisks := map[string]bool{}
	for _, s := range to.Doc.Sections {
		newSections[s.ID] = true
		switch title, ok := oldSections[s.ID]; {
		case !ok:
			d.SectionsAdded = append(d.SectionsAdded, sectionRef{s.ID, s.Title})
		case title != s.Title:
			d.SectionsRenamed = append(d.SectionsRenamed, sectionRename{s.ID, title, s.Title})
		default:
			d.SectionsUnchanged++
		}
		for _, h := range s.Hunks {
			newHunks[h] = true
			if was, ok := oldHunks[h]; ok && was != s.ID {
				d.HunksMoved = append(d.HunksMoved, hunkMove{h, paths[h], was, s.ID})
			} else if !ok {
				d.HunksAdded++
			}
		}
		for _, r := range s.Risks {
			newRisks[r.ID] = true
			ref := riskRef{s.ID, r.ID, r.Tier, r.Text}
			old, existed := oldRisks[r.ID]
			mitigated := r.State == "mitigated" || r.MitigatedVersion != nil
			switch {
			case !existed:
				d.RisksAdded = append(d.RisksAdded, ref)
			case mitigated && old.state != "mitigated":
				d.RisksMitigated = append(d.RisksMitigated, ref)
			}
		}
	}
	for _, s := range from.Doc.Sections {
		if !newSections[s.ID] {
			d.SectionsRemoved = append(d.SectionsRemoved, sectionRef{s.ID, s.Title})
		}
		for _, h := range s.Hunks {
			if !newHunks[h] {
				d.HunksRemoved++
			}
		}
		for _, r := range s.Risks {
			if !newRisks[r.ID] {
				d.RisksRemoved = append(d.RisksRemoved, riskRef{s.ID, r.ID, r.Tier, r.Text})
			}
		}
	}
	decision := func(v versionShape) any {
		if v.Doc.Summary.Verdict == nil {
			return nil
		}
		return v.Doc.Summary.Verdict.Decision
	}
	if a, b := decision(from), decision(to); a != b {
		d.Recommendation = map[string]any{"from": a, "to": b}
	}
	score := func(v versionShape) any {
		var rs struct {
			Score *int `json:"score"`
		}
		if json.Unmarshal(v.Doc.Summary.RiskScore, &rs) != nil || rs.Score == nil {
			return nil
		}
		return *rs.Score
	}
	if a, b := score(from), score(to); a != b {
		d.RiskScore = map[string]any{"from": a, "to": b}
	}
	return d
}

// relatedPull is one related pull request as related_pull_requests lists it.
type relatedPull struct {
	Number      int      `json:"number"`
	Title       string   `json:"title,omitempty"`
	State       string   `json:"state,omitempty"`
	Author      string   `json:"author,omitempty"`
	Draft       bool     `json:"draft,omitempty"`
	HeadRef     string   `json:"head_ref,omitempty"`
	BaseRef     string   `json:"base_ref,omitempty"`
	MergedAt    string   `json:"merged_at,omitempty"`
	Commit      string   `json:"commit,omitempty"`
	SavedReview bool     `json:"saved_review"`
	Repository  string   `json:"repository,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	URL         string   `json:"url,omitempty"`
	Shared      []string `json:"shared_files,omitempty"`
	Why         string   `json:"why,omitempty"`
}

func related(p indexPull, why string) relatedPull {
	return relatedPull{Number: p.Number, Title: p.Title, State: p.State, Author: p.Author, Draft: p.Draft, HeadRef: p.HeadRef, BaseRef: p.BaseRef,
		SavedReview: p.VersionCount > 0, URL: p.URL, Why: why}
}

// maxCompared bounds the open pull requests compared in the clone.
const maxCompared = 30

func callRelatedPullRequests(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Paths []string `json:"paths"`
		State string   `json:"state"`
		Limit int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.State == "" {
		a.State = "all"
	}
	limit := boundRows(a.Limit, 20, 50)
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	pulls := c.indexPulls(ctx, repository)
	byNumber := map[int]indexPull{}
	for _, p := range pulls {
		byNumber[p.Number] = p
	}
	this, known := byNumber[number]
	var notes []string
	out := map[string]any{"repository": repository, "pull_request": number}

	// The stack: by branches, and the stack groups it is in.
	stack := map[string]any{}
	if known {
		var parents, children []relatedPull
		for _, p := range pulls {
			if p.Number == number || p.Historical {
				continue
			}
			if this.BaseRef != "" && p.HeadRef == this.BaseRef {
				parents = append(parents, related(p, "its head branch is this pull request's base"))
			}
			if this.HeadRef != "" && p.BaseRef == this.HeadRef {
				children = append(children, related(p, "based on this pull request's head branch"))
			}
		}
		stack["based_on"], stack["based_on_this"] = emptyList(parents), emptyList(children)
	} else {
		notes = append(notes, "the pull request is not in baum's index, so its branches are unknown")
	}
	if groups, err := c.groupMembers(ctx, repository, number); err == nil {
		stack["groups"] = groups
	}
	out["stack"] = stack

	// What mentions it on GitHub.
	if refs, err := c.mentions(ctx, repository, number, byNumber); err != nil {
		notes = append(notes, "mentions unavailable: "+err.Error())
	} else {
		out["mentioned_by"] = refs
	}

	// The same files, open and recently merged.
	if p, err := c.clonePull(ctx, args); err != nil {
		notes = append(notes, "same files not compared: "+err.Error())
	} else {
		paths := a.Paths
		if len(paths) == 0 {
			source, err := c.sourceOf(ctx, p.entry, true)
			if err != nil {
				notes = append(notes, "same files not compared: "+err.Error())
			}
			for _, f := range changedFiles(source.Hunks) {
				paths = append(paths, f.Path)
			}
		}
		if len(paths) > 0 {
			same, more := c.sameFiles(ctx, p, number, paths, pulls, byNumber, a.State, limit)
			out["same_files"] = same
			notes = append(notes, more...)
		}
	}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return compactJSON(out)
}

// groupMembers are the review groups and stacks the pull request is in, with
// their members.
func (c CallContext) groupMembers(ctx context.Context, repository string, number int) ([]map[string]any, error) {
	body, _, err := c.get(ctx, "/api/groups", nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Groups []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Stack      bool   `json:"stack"`
			Repository string `json:"repository"`
			Members    []int  `json:"members"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range list.Groups {
		if strings.EqualFold(g.Repository, repository) && slices.Contains(g.Members, number) {
			out = append(out, map[string]any{"id": g.ID, "name": g.Name, "stack": g.Stack, "members": g.Members})
		}
	}
	return out, nil
}

// mentions are the issues and pull requests whose text mentions the pull
// request, and the commits that reference it, from its GitHub timeline.
func (c CallContext) mentions(ctx context.Context, repository string, number int, byNumber map[int]indexPull) ([]relatedPull, error) {
	body, missing, err := c.get(ctx, "/api/prs/timeline", pullQuery(repository, number))
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, errors.New("the timeline is not available")
	}
	var timeline struct {
		Events []struct {
			Type      string `json:"type"`
			At        string `json:"at"`
			CommitOID string `json:"commit_oid"`
			Repo      string `json:"repository"`
			Source    *struct {
				Kind       string `json:"kind"`
				Repository string `json:"repository"`
				Number     int    `json:"number"`
				Title      string `json:"title"`
				State      string `json:"state"`
				URL        string `json:"url"`
			} `json:"source"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &timeline); err != nil {
		return nil, errors.New("the timeline is not readable")
	}
	out := []relatedPull{}
	for _, e := range timeline.Events {
		switch {
		case e.Type == "cross_referenced" && e.Source != nil:
			r := relatedPull{Number: e.Source.Number, Title: e.Source.Title, State: strings.ToLower(e.Source.State), Kind: e.Source.Kind, URL: e.Source.URL, Why: "mentions this pull request"}
			if !strings.EqualFold(e.Source.Repository, repository) {
				r.Repository = e.Source.Repository
			} else if p, ok := byNumber[e.Source.Number]; ok && e.Source.Kind == "pull_request" {
				r.SavedReview, r.Author, r.HeadRef, r.BaseRef = p.VersionCount > 0, p.Author, p.HeadRef, p.BaseRef
			}
			out = append(out, r)
		case e.Type == "referenced" && e.CommitOID != "":
			out = append(out, relatedPull{Commit: e.CommitOID, Repository: e.Repo, Kind: "commit", Why: "a commit message references this pull request"})
		}
	}
	return out, nil
}

// sameFiles finds the open pull requests whose changes share paths with
// this one's, compared in the clone, and the merged ones that touched them
// lately, from the base branch's history.
func (c CallContext) sameFiles(ctx context.Context, p *clonePull, number int, paths []string, pulls []indexPull, byNumber map[int]indexPull, state string, limit int) (map[string]any, []string) {
	var notes []string
	if len(paths) > 50 {
		notes = append(notes, fmt.Sprintf("compared the first 50 of %d paths", len(paths)))
		paths = paths[:50]
	}
	wanted := map[string]bool{}
	for _, path := range paths {
		wanted[path] = true
	}
	out := map[string]any{"paths": paths}
	header, err := p.reviewHeader(ctx, c)
	base := header.Base
	if base == "" {
		base = header.MergeBase
	}
	if err != nil || base == "" {
		notes = append(notes, "the pull request's base is not known: nothing compared")
		return out, notes
	}
	if state == "open" || state == "all" {
		open := []relatedPull{}
		compared, skipped := 0, 0
		for _, other := range pulls {
			if other.Number == number || other.Historical || other.State != "open" || other.HeadSHA == "" {
				continue
			}
			if compared == maxCompared || len(open) == limit {
				skipped++
				continue
			}
			compared++
			q := url.Values{}
			for k, v := range p.query {
				q[k] = v
			}
			q.Set("base", base)
			q.Set("head", other.HeadSHA)
			body, missing, err := c.get(ctx, "/api/clone/changed", q)
			if err != nil || missing {
				skipped++
				continue
			}
			var changed struct {
				Files []string `json:"files"`
			}
			if json.Unmarshal(body, &changed) != nil {
				skipped++
				continue
			}
			var shared []string
			for _, f := range changed.Files {
				if wanted[f] {
					shared = append(shared, f)
				}
			}
			if len(shared) > 0 {
				r := related(other, "changes the same files")
				r.Shared = shared
				open = append(open, r)
			}
		}
		out["open"] = open
		if skipped > 0 {
			notes = append(notes, fmt.Sprintf("%d open pull requests not compared (the clone lacks their head, or past the limit of %d)", skipped, maxCompared))
		}
	}
	if state == "merged" || state == "all" {
		q := url.Values{}
		for k, v := range p.query {
			q[k] = v
		}
		q.Set("rev", base)
		q.Set("limit", "200")
		q.Set("since", time.Now().AddDate(0, -3, 0).Format(time.DateOnly))
		for _, path := range paths {
			q.Add("path", path)
		}
		body, _, err := c.get(ctx, "/api/clone/log", q)
		var log struct {
			Commits []struct {
				SHA     string `json:"sha"`
				Date    string `json:"date"`
				Subject string `json:"subject"`
			} `json:"commits"`
		}
		if err != nil || json.Unmarshal(body, &log) != nil {
			notes = append(notes, "merged pull requests not found: the base branch's history is not readable")
		} else {
			links := pullLinks{heads: map[string]int{}}
			merged := []relatedPull{}
			seen := map[int]bool{}
			for _, commit := range log.Commits {
				n, _ := links.of(commit.SHA, commit.Subject)
				if n == 0 || n == number || seen[n] || len(merged) == limit {
					continue
				}
				seen[n] = true
				r := relatedPull{Number: n, Title: commit.Subject, State: "merged", MergedAt: commit.Date, Commit: commit.SHA, Why: "merged into the base in the last 3 months, touching the same files"}
				if known, ok := byNumber[n]; ok {
					r.Title, r.Author, r.HeadRef, r.BaseRef, r.SavedReview, r.URL = known.Title, known.Author, known.HeadRef, known.BaseRef, known.VersionCount > 0, known.URL
				}
				merged = append(merged, r)
			}
			out["merged"] = merged
		}
	}
	return out, notes
}

func callValidationRuns(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	body, from, err := c.pullDocument(ctx, e)
	if err != nil {
		return "", err
	}
	var doc struct {
		Review docHeader `json:"review"`
		Stats  struct {
			Validation json.RawMessage `json:"validation"`
		} `json:"stats"`
		Index struct {
			Validation []struct {
				ID            string          `json:"id"`
				Session       string          `json:"session"`
				Method        string          `json:"method"`
				Checked       string          `json:"checked"`
				Outcome       string          `json:"outcome"`
				EvidenceClass string          `json:"evidence_class"`
				Caveats       string          `json:"caveats,omitempty"`
				CommitLabel   json.RawMessage `json:"commit_label"`
				EventIDs      []string        `json:"event_ids"`
			} `json:"validation"`
			Sessions []struct {
				ID                    string          `json:"id"`
				Vendor                string          `json:"vendor"`
				Title                 json.RawMessage `json:"title"`
				Restricted            bool            `json:"restricted"`
				NotVerified           json.RawMessage `json:"not_verified,omitempty"`
				SetAside              json.RawMessage `json:"set_aside,omitempty"`
				ValidationUnavailable string          `json:"validation_unavailable,omitempty"`
				Events                []struct {
					ID      string `json:"id"`
					At      string `json:"at"`
					Verb    string `json:"verb"`
					Summary string `json:"summary"`
				} `json:"events"`
			} `json:"sessions"`
		} `json:"index"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", errors.New("the pull request's review is not readable")
	}
	cited := map[string]bool{}
	for _, v := range doc.Index.Validation {
		for _, id := range v.EventIDs {
			cited[id] = true
		}
	}
	sessions := []map[string]any{}
	for _, s := range doc.Index.Sessions {
		if s.Restricted {
			continue
		}
		row := map[string]any{"id": s.ID, "vendor": s.Vendor}
		if len(s.Title) > 0 && string(s.Title) != "null" {
			row["title"] = s.Title
		}
		if len(s.NotVerified) > 0 {
			row["not_verified"] = s.NotVerified
		}
		if len(s.SetAside) > 0 {
			row["set_aside"] = s.SetAside
		}
		if s.ValidationUnavailable != "" {
			row["validation_unavailable"] = s.ValidationUnavailable
		}
		var events []any
		for _, ev := range s.Events {
			if cited[ev.ID] {
				events = append(events, ev)
			}
		}
		if len(events) > 0 {
			row["cited_events"] = events
		}
		sessions = append(sessions, row)
	}
	out := map[string]any{"repository": repository, "number": number, "from": from, "head": doc.Review.Head,
		"checks": emptyList(doc.Index.Validation), "sessions": sessions}
	if from == "review" {
		out["version"] = doc.Review.Version
	}
	if len(doc.Stats.Validation) > 0 && string(doc.Stats.Validation) != "null" {
		out["stats"] = doc.Stats.Validation
	}
	if len(doc.Index.Validation) == 0 && len(doc.Stats.Validation) == 0 {
		out["note"] = "no validation was extracted for the linked sessions: extraction is off, found nothing, or no session is linked"
	}
	return compactJSON(out)
}
