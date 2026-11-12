package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/inboxqueue"
	"github.com/baum-ai/baum/internal/prfilter"
	"github.com/baum-ai/baum/internal/reviewindex"
)

// The tools of the Inbox and Pull requests pages. They read the routes the
// pages read (/api/inbox, /api/inboxes, /api/index and the saved filters)
// and show the pull requests as the lists do. All are global: they list
// pull requests of every repository, so an action never sees them.

const (
	defaultRows = 50
	maxRows     = 200
	maxMatches  = 100
)

// boundRows is n, or def when it is zero, at most max.
func boundRows(n, def, max int) int {
	if n <= 0 {
		n = def
	}
	return min(n, max)
}

// pageFilter is a filter of the page the user has open, when it is one of pages.
func (c CallContext) pageFilter(name string, pages ...string) string {
	if !c.HasPage || !slices.Contains(pages, c.Page.Page) {
		return ""
	}
	return strings.TrimSpace(c.Page.Filters[name])
}

func (s *Server) listTools() []Tool {
	return []Tool{{
		Name: "inbox",
		Description: "The pull requests in the app's Inbox, as Up next and an inbox's queue show them: what each waits for (kind: review, clone, fix, merge, update, other for someone else's that waits on nothing from the person, waiting for their own that waits on others), whether an agent is fixing it, snoozed or archived, the saved inboxes holding it, its risk, checks and review state. " +
			"Defaults to the inbox the page has selected; with none, the queue waiting on the person. Pass limit for more (at most 200).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"inbox":{"type":"string","description":"A saved inbox's id (see inboxes). Default: the page's selected inbox, else the whole queue."},` +
			`"kind":{"type":"string","enum":["review","clone","fix","merge","update","other","waiting"],"description":"Only pull requests of this kind. other (someone else's that wait on nothing from the person) and waiting (their own that wait on others) are not in the queue, so asking for one lists them from every open pull request."},` +
			`"query":{"type":"string","maxLength":1000,"description":"Only those an inbox filter query holds, as test_query reads it."},` +
			`"hidden":{"type":"boolean","description":"Include the snoozed and archived ones. Default false."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most to return. Default 50."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/inbox", "/api/index"},
		Call:  callInbox,
	}, {
		Name:        "inboxes",
		Description: "The person's saved inboxes as the app lists them: each one's id, name, colour, what it holds, owners, risk, repositories, filter query, whether bots are left out, whether it badges the sidebar, and how many pull requests it holds now.",
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/inboxes", "/api/inbox"},
		Call:        callInboxes,
	}, {
		Name: "test_query",
		Description: "Tries an inbox filter query on the open pull requests, as the app's filter box does before it is saved: the pull requests it holds now (at most 100, with how many more), or why the query does not read (problem, with its message). " +
			"The query is words and fields such as repo=acme/web author=@me risk>=3 waiting=review, joined by AND, OR and parentheses.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":1000,"description":"The filter query to try."}},"required":["query"],"additionalProperties":false}`),
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/inbox", "/api/index"},
		Call:        callTestQuery,
	}, {
		Name: "pull_requests",
		Description: "The Pull requests page's list: the open pull requests, or those closed (history), or a saved tab, narrowed by the filter query the page holds (or a query given), as the app shows them: title, author, repository, review, merge and check state, risk and review time. " +
			"Defaults to the page's tab and filter. Pass limit for more (at most 200); the rest are counted.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"view":{"type":"string","enum":["open","closed"],"description":"Open pull requests, or Closed (merged, closed or no longer listed, whose reviews were saved). Default: the page's, else open."},` +
			`"tab":{"type":"string","description":"A saved tab's id: its view and filter."},` +
			`"query":{"type":"string","maxLength":1000,"description":"A filter query in place of the page's own, as test_query reads it."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":200,"description":"The most to return. Default 50."}},"additionalProperties":false}`),
		Scope: ScopeGlobal,
		Reads: []string{"/api/index", "/api/ui-state/filters"},
		Call:  callPullRequests,
	}}
}

// pullRow is a pull request as the lists show it.
type pullRow struct {
	Repository    string          `json:"repository"`
	Number        int             `json:"number"`
	Title         string          `json:"title"`
	Author        string          `json:"author,omitempty"`
	URL           string          `json:"url,omitempty"`
	State         string          `json:"state,omitempty"`
	Draft         bool            `json:"draft,omitempty"`
	HeadRef       string          `json:"head_ref,omitempty"`
	BaseRef       string          `json:"base_ref,omitempty"`
	Additions     int             `json:"additions,omitempty"`
	Deletions     int             `json:"deletions,omitempty"`
	ChangedFiles  int             `json:"changed_files,omitempty"`
	ReviewStatus  string          `json:"baum_review"`
	VersionCount  int             `json:"review_versions"`
	Review        string          `json:"review,omitempty"`
	Merge         string          `json:"merge,omitempty"`
	Checks        string          `json:"checks,omitempty"`
	Unresolved    *int            `json:"unresolved_threads,omitempty"`
	ReviewMinutes *int            `json:"review_minutes,omitempty"`
	Risk          json.RawMessage `json:"risk,omitempty"`
	Historical    bool            `json:"historical,omitempty"`
}

func rowOf(e reviewindex.Entry) pullRow {
	pr := e.PullRequest
	row := pullRow{Repository: pr.Repository, Number: pr.Number, Title: pr.Title, Author: pr.Author, URL: pr.URL, State: pr.State,
		Draft: pr.Draft, HeadRef: pr.HeadRef, BaseRef: pr.BaseRef, Additions: pr.Additions, Deletions: pr.Deletions, ChangedFiles: pr.ChangedFiles,
		ReviewStatus: e.Status, VersionCount: e.VersionCount, Review: e.Review, Merge: e.Merge, Checks: e.Checks,
		Unresolved: e.Unresolved, ReviewMinutes: e.ReviewMinutes, Historical: e.Historical}
	if raw := strings.TrimSpace(string(e.RiskScore)); raw != "" && raw != "null" {
		row.Risk = e.RiskScore
	}
	return row
}

// indexEntries are the review index's entries, with the saved-only ones when
// history is set, one per pull request: of the clones that hold one, the
// first (the lists show a pull request once).
func (c CallContext) indexEntries(ctx context.Context, history bool) ([]reviewindex.Entry, error) {
	query := url.Values(nil)
	if history {
		query = url.Values{"history": {"1"}}
	}
	body, _, err := c.get(ctx, "/api/index", query)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errIndexUnavailable, err)
	}
	var index struct {
		Available *bool               `json:"available"`
		Entries   []reviewindex.Entry `json:"entries"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, errors.New("the review index is not readable")
	}
	if index.Available != nil && !*index.Available {
		return nil, errIndexUnavailable
	}
	seen := map[string]bool{}
	out := index.Entries[:0:0]
	for _, e := range index.Entries {
		key := inboxqueue.Key(e)
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// queueItem is one item of /api/inbox's answer.
type queueItem struct {
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Fixing   bool     `json:"fixing"`
	Snoozed  bool     `json:"snoozed"`
	Archived bool     `json:"archived"`
	Inboxes  []string `json:"inboxes"`
}

func (i queueItem) hidden() bool { return i.Snoozed || i.Archived }

// queued is inboxqueue.Item.Queued.
func (i queueItem) queued() bool {
	return i.Kind != inboxqueue.Waiting && i.Kind != inboxqueue.Other && !i.Fixing
}

// queueInbox is a saved inbox as the queue counts it.
type queueInbox struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	Count int    `json:"count"`
}

type queue struct {
	Available *bool        `json:"available"`
	Count     int          `json:"count"`
	Items     []queueItem  `json:"items"`
	Inboxes   []queueInbox `json:"inboxes"`
}

func (c CallContext) queue(ctx context.Context) (queue, error) {
	body, _, err := c.get(ctx, "/api/inbox", nil)
	if err != nil {
		return queue{}, fmt.Errorf("the inbox is unavailable: %w", err)
	}
	var q queue
	if err := json.Unmarshal(body, &q); err != nil {
		return queue{}, errors.New("the inbox is not readable")
	}
	if q.Available != nil && !*q.Available {
		return queue{}, errors.New("the inbox is unavailable: the daemon has none")
	}
	return q, nil
}

// kindName is a queue item's kind as the tools name it: "" is a pull request
// of the person's own that waits on others.
func kindName(kind string) string {
	if kind == inboxqueue.Waiting {
		return "waiting"
	}
	return kind
}

// inboxRow is a pull request of the Inbox.
type inboxRow struct {
	pullRow
	Kind     string   `json:"kind"`
	Fixing   bool     `json:"fixing,omitempty"`
	Snoozed  bool     `json:"snoozed,omitempty"`
	Archived bool     `json:"archived,omitempty"`
	Inboxes  []string `json:"inboxes"`
}

// rowsOf joins queue items with the index's entries, in the queue's order.
func rowsOf(items []queueItem, entries []reviewindex.Entry) []inboxRow {
	byKey := map[string]reviewindex.Entry{}
	for _, e := range entries {
		byKey[inboxqueue.Key(e)] = e
	}
	rows := []inboxRow{}
	for _, item := range items {
		e, ok := byKey[item.Key]
		if !ok {
			continue
		}
		inboxes := item.Inboxes
		if inboxes == nil {
			inboxes = []string{}
		}
		rows = append(rows, inboxRow{pullRow: rowOf(e), Kind: kindName(item.Kind), Fixing: item.Fixing, Snoozed: item.Snoozed, Archived: item.Archived, Inboxes: inboxes})
	}
	return rows
}

func callInbox(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Inbox  string `json:"inbox"`
		Kind   string `json:"kind"`
		Query  string `json:"query"`
		Hidden bool   `json:"hidden"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Inbox == "" {
		a.Inbox = c.pageFilter("inbox", PageInbox)
	}
	q, err := c.queue(ctx)
	if err != nil {
		return "", err
	}
	if a.Inbox != "" && !slices.ContainsFunc(q.Inboxes, func(i queueInbox) bool { return i.ID == a.Inbox }) {
		return "", fmt.Errorf("there is no inbox %q: the inboxes tool lists them", a.Inbox)
	}
	var matching map[string]bool
	if a.Query != "" {
		draft, err := c.draft(ctx, a.Query)
		if err != nil {
			return "", err
		}
		if draft.Problem != "" {
			return "", errors.New("the query does not read: " + draft.Problem)
		}
		matching = map[string]bool{}
		for _, key := range draft.Matches {
			matching[key] = true
		}
	}
	items := []queueItem{}
	for _, item := range q.Items {
		switch {
		case a.Inbox != "" && !slices.Contains(item.Inboxes, a.Inbox):
		// An inbox lists what the queue holds and others' pull requests; the
		// queue itself only what waits on the person.
		case a.Inbox != "" && !item.queued() && item.Kind != inboxqueue.Other:
		// Asking for a kind (other, waiting) looks beyond the queue.
		case a.Inbox == "" && a.Kind == "" && !item.queued():
		case !a.Hidden && item.hidden():
		case a.Kind != "" && kindName(item.Kind) != a.Kind:
		case matching != nil && !matching[item.Key]:
		default:
			items = append(items, item)
		}
	}
	entries, err := c.indexEntries(ctx, false)
	if err != nil {
		return "", err
	}
	rows := rowsOf(items, entries)
	limit := boundRows(a.Limit, defaultRows, maxRows)
	more := max(0, len(rows)-limit)
	rows = rows[:min(len(rows), limit)]
	return compactJSON(struct {
		Waiting int        `json:"waiting"`
		Inbox   string     `json:"inbox,omitempty"`
		Total   int        `json:"total"`
		More    int        `json:"more,omitempty"`
		Items   []inboxRow `json:"items"`
	}{q.Count, a.Inbox, len(rows) + more, more, rows})
}

func callInboxes(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	body, _, err := c.get(ctx, "/api/inboxes", nil)
	if err != nil {
		return "", fmt.Errorf("the saved inboxes are unavailable: %w", err)
	}
	if disabled(body) {
		return "", errors.New("the saved inboxes are unavailable: the daemon has none")
	}
	var saved struct {
		Revision int               `json:"revision"`
		Inboxes  []json.RawMessage `json:"inboxes"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		return "", errors.New("the saved inboxes are not readable")
	}
	// The daemon's own counts: what each inbox holds now. Without them the
	// inboxes are listed with no count.
	counts := map[string]int{}
	folded := map[string]string{}
	if q, err := c.queue(ctx); err == nil {
		for _, i := range q.Inboxes {
			counts[i.ID], folded[i.ID] = i.Count, i.Query
		}
	}
	list := []map[string]json.RawMessage{}
	for _, raw := range saved.Inboxes {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", errors.New("the saved inboxes are not readable")
		}
		var id string
		_ = json.Unmarshal(fields["id"], &id)
		if count, ok := counts[id]; ok {
			fields["count"], _ = json.Marshal(count)
			fields["filter"], _ = json.Marshal(folded[id])
		}
		list = append(list, fields)
	}
	return compactJSON(struct {
		Revision int                          `json:"revision"`
		Inboxes  []map[string]json.RawMessage `json:"inboxes"`
	}{saved.Revision, list})
}

// draft is the daemon's answer to /api/inbox?query=: the keys of the items an
// inbox with the query would hold, or why the query does not read.
type draft struct {
	Problem string   `json:"problem"`
	Matches []string `json:"matches"`
}

func (c CallContext) draft(ctx context.Context, query string) (draft, error) {
	body, _, err := c.get(ctx, "/api/inbox", url.Values{"query": {query}})
	if err != nil {
		return draft{}, fmt.Errorf("the query could not be tried: %w", err)
	}
	var d draft
	if err := json.Unmarshal(body, &d); err != nil || disabled(body) {
		return draft{}, errors.New("the query could not be tried: the inbox is unavailable")
	}
	return d, nil
}

func callTestQuery(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// The daemon tells whether the query reads (prfilter.Problem) and which
	// pull requests of the queue it holds (prfilter.Compile), so the tool
	// shows what the inbox filter box would; the query is also checked here,
	// before anything is read.
	if problem := prfilter.Problem(a.Query); problem != "" {
		return compactJSON(struct {
			Query   string     `json:"query"`
			Valid   bool       `json:"valid"`
			Problem string     `json:"problem"`
			Total   int        `json:"total"`
			Matches []inboxRow `json:"matches"`
		}{a.Query, false, problem, 0, []inboxRow{}})
	}
	d, err := c.draft(ctx, a.Query)
	if err != nil {
		return "", err
	}
	if d.Problem != "" {
		return compactJSON(struct {
			Query   string     `json:"query"`
			Valid   bool       `json:"valid"`
			Problem string     `json:"problem"`
			Total   int        `json:"total"`
			Matches []inboxRow `json:"matches"`
		}{a.Query, false, d.Problem, 0, []inboxRow{}})
	}
	q, err := c.queue(ctx)
	if err != nil {
		return "", err
	}
	entries, err := c.indexEntries(ctx, false)
	if err != nil {
		return "", err
	}
	held := map[string]bool{}
	for _, key := range d.Matches {
		held[key] = true
	}
	var items []queueItem
	for _, item := range q.Items {
		// The app's preview matches the listed items: those in the queue and
		// others' pull requests, neither snoozed nor archived.
		if held[item.Key] && !item.hidden() && (item.queued() || item.Kind == inboxqueue.Other) {
			items = append(items, item)
		}
	}
	rows := rowsOf(items, entries)
	more := max(0, len(rows)-maxMatches)
	rows = rows[:min(len(rows), maxMatches)]
	return compactJSON(struct {
		Query   string     `json:"query"`
		Valid   bool       `json:"valid"`
		Total   int        `json:"total"`
		More    int        `json:"more,omitempty"`
		Matches []inboxRow `json:"matches"`
	}{a.Query, true, len(rows) + more, more, rows})
}

// savedTab is a Pull requests tab saved from a view and its filter.
type savedTab struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	View  string `json:"view"`
	Query string `json:"query"`
}

// pullsFilters are the saved filters: each page's draft query and the tabs.
func (c CallContext) pullsFilters(ctx context.Context) (pages map[string]string, tabs []savedTab) {
	body, _, err := c.get(ctx, "/api/ui-state/filters", nil)
	if err != nil || disabled(body) {
		return nil, nil
	}
	var record struct {
		Value *struct {
			Pages map[string]string `json:"pages"`
			Tabs  []savedTab        `json:"tabs"`
		} `json:"value"`
	}
	if json.Unmarshal(body, &record) != nil || record.Value == nil {
		return nil, nil
	}
	return record.Value.Pages, record.Value.Tabs
}

func callPullRequests(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		View  string  `json:"view"`
		Tab   string  `json:"tab"`
		Query *string `json:"query"`
		Limit int     `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// Which tab: the one asked for, else the page's (filter=open, history or
	// tab:<id>), else Open.
	filter := "open"
	switch {
	case a.Tab != "":
		filter = "tab:" + a.Tab
	case a.View == "closed":
		filter = "history"
	case a.View == "open":
	default:
		if f := c.pageFilter("filter", PagePulls); f != "" {
			filter = f
		}
	}
	view, tabID, query := "open", "", ""
	pageOwn := c.HasPage && c.Page.Page == PagePulls
	if id, ok := strings.CutPrefix(filter, "tab:"); ok {
		pages, tabs := c.pullsFilters(ctx)
		i := slices.IndexFunc(tabs, func(t savedTab) bool { return t.ID == id })
		switch {
		case i >= 0:
			tabID, view, query = id, tabs[i].View, tabs[i].Query
			// The page narrows a tab with a draft until it is saved.
			if draft, ok := pages["tab:"+id]; ok && pageOwn && a.Tab == "" {
				query = draft
			}
		case a.Tab != "":
			return "", fmt.Errorf("there is no saved tab %q", a.Tab)
		}
	} else {
		if filter == "history" {
			view = "history"
		}
		if pageOwn && a.View == "" {
			pages, _ := c.pullsFilters(ctx)
			query = pages["pulls"]
		}
	}
	if view != "history" {
		view = "open"
	}
	if a.Query != nil {
		query = *a.Query
	}
	query = strings.TrimSpace(query)
	if problem := prfilter.Problem(query); problem != "" {
		return "", errors.New("the filter does not read: " + problem)
	}
	entries, err := c.indexEntries(ctx, true)
	if err != nil {
		return "", err
	}
	test := prfilter.Compile(query)
	rows := []pullRow{}
	for _, e := range entries {
		if e.Historical == (view == "history") && test(e, "") {
			rows = append(rows, rowOf(e))
		}
	}
	limit := boundRows(a.Limit, defaultRows, maxRows)
	more := max(0, len(rows)-limit)
	total := len(rows)
	rows = rows[:min(len(rows), limit)]
	return compactJSON(struct {
		View         string    `json:"view"`
		Tab          string    `json:"tab,omitempty"`
		Query        string    `json:"query,omitempty"`
		Total        int       `json:"total"`
		More         int       `json:"more,omitempty"`
		PullRequests []pullRow `json:"pull_requests"`
	}{map[string]string{"open": "open", "history": "closed"}[view], tabID, query, total, more, rows})
}
