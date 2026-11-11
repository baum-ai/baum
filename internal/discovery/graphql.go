package discovery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// GitHub gives up on a GraphQL query after about ten seconds and answers with
// a gateway error. The inbox fields take about 0.2 seconds per pull request on
// a busy repository, so a query asks for at most inboxPageSize of them, and one
// GitHub gave up on is asked again for fewer, keeping what was already read.
const (
	inboxPageSize     = 25
	inboxMinPageSize  = 5
	inboxPageAttempts = 4
)

// inboxRetryDelay is the pause before a query's second attempt; later attempts
// wait longer.
var inboxRetryDelay = 2 * time.Second

// rollup is a commit's checks: GitHub's combined state, and how many of its
// check runs and commit statuses are in each state.
type rollup struct {
	State    string
	Contexts *struct {
		CheckRunCountsByState      []stateCount
		StatusContextCountsByState []stateCount
	}
}

type stateCount struct {
	State string
	Count int
}

// failedRuns are the check run and commit status states that fail a commit's
// checks, as the fix reads them (internal/fix).
var failedRuns = map[string]bool{
	"FAILURE": true, "TIMED_OUT": true, "STARTUP_FAILURE": true, "ACTION_REQUIRED": true, "ERROR": true,
}

// failed is whether any check run or commit status already failed, while
// others may still run.
func (r *rollup) failed() bool {
	if r.Contexts == nil {
		return false
	}
	for _, counts := range [][]stateCount{r.Contexts.CheckRunCountsByState, r.Contexts.StatusContextCountsByState} {
		for _, c := range counts {
			if c.Count > 0 && failedRuns[c.State] {
				return true
			}
		}
	}
	return false
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}
type gqlRepository struct {
	DatabaseID    int64
	NameWithOwner string
}
type gqlPR struct {
	Number int
	// State is OPEN, CLOSED or MERGED; a query of open ones may leave it out.
	State                                       string `json:",omitempty"`
	Title, HeadRefOid, HeadRefName, BaseRefName string
	IsDraft                                     bool
	// Additions, Deletions and ChangedFiles size the change; they take part
	// in the unchanged comparison like the head.
	Additions, Deletions, ChangedFiles int
	// UpdatedAt orders the incremental reads and feeds the cursor, the
	// probe's fingerprint and the snapshot; it is left out of the unchanged
	// comparison so the backoff is unaffected.
	UpdatedAt      time.Time `json:",omitzero"`
	Author         actor
	HeadRepository *gqlRepository
	BaseRepository gqlRepository
	Assignees      struct{ Nodes []actor }
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Typename   string `json:"__typename"`
				Login      string
				DatabaseID int64
			}
		}
		PageInfo pageInfo
	}
	LatestReviews *struct {
		Nodes    []struct{ State string }
		PageInfo struct{ HasPreviousPage bool }
	}
	ReviewDecision           *string
	LatestOpinionatedReviews *struct {
		Nodes    []struct{ State string }
		PageInfo pageInfo
	}
	Commits *struct {
		Nodes []struct {
			Commit struct{ StatusCheckRollup *rollup }
		}
	}
	Mergeable, MergeStateStatus string
	ReviewThreads               *struct {
		Nodes    []struct{ IsResolved bool }
		PageInfo pageInfo
	}
}

func (pr gqlPR) legacy() apiPR {
	var out apiPR
	out.Number, out.Title, out.Draft, out.User = pr.Number, pr.Title, pr.IsDraft, pr.Author
	out.Head.SHA, out.Head.Ref = pr.HeadRefOid, pr.HeadRefName
	if pr.HeadRepository != nil {
		out.Head.Repo = &struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		}{pr.HeadRepository.DatabaseID, pr.HeadRepository.NameWithOwner}
	}
	out.Base.Ref = pr.BaseRefName
	out.Base.Repo.ID, out.Base.Repo.FullName = pr.BaseRepository.DatabaseID, pr.BaseRepository.NameWithOwner
	for _, node := range pr.ReviewRequests.Nodes {
		reviewer := node.RequestedReviewer
		if reviewer.Typename == "User" {
			out.RequestedReviewers = append(out.RequestedReviewers, actor{Login: reviewer.Login})
		}
		if reviewer.Typename == "Team" {
			out.RequestedTeams = append(out.RequestedTeams, team{ID: reviewer.DatabaseID})
		}
	}
	return out
}

// behind reports a head branch out of date with its base that Update branch
// can fix: GitHub says BEHIND, and it is neither a draft nor conflicting.
func (pr gqlPR) behind() bool {
	return !pr.IsDraft && pr.Mergeable != "CONFLICTING" && pr.MergeStateStatus == "BEHIND"
}

func (pr gqlPR) states() (review, checks, merge string, unresolved *int, capped bool) {
	if pr.ReviewDecision != nil {
		switch *pr.ReviewDecision {
		case "APPROVED":
			review = "approved"
		case "CHANGES_REQUESTED":
			review = "changes_requested"
		case "REVIEW_REQUIRED":
			review = "unreviewed"
		}
	}
	if pr.ReviewDecision == nil || review == "unreviewed" {
		if pr.LatestOpinionatedReviews != nil && !pr.LatestOpinionatedReviews.PageInfo.HasNextPage {
			fallback := "unreviewed"
			for _, node := range pr.LatestOpinionatedReviews.Nodes {
				switch node.State {
				case "CHANGES_REQUESTED":
					fallback = "changes_requested"
				case "APPROVED":
					if fallback != "changes_requested" {
						fallback = "approved"
					}
				case "COMMENTED":
					if fallback == "unreviewed" {
						fallback = "commented"
					}
				}
			}
			if review == "" || fallback == "commented" {
				review = fallback
			}
		}
	}
	if review == "unreviewed" && pr.LatestReviews != nil {
		for _, node := range pr.LatestReviews.Nodes {
			if node.State == "COMMENTED" {
				review = "commented"
				break
			}
		}
	}
	if pr.Commits != nil && len(pr.Commits.Nodes) > 0 {
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		if rollup == nil {
			checks = "none"
		} else {
			switch rollup.State {
			case "SUCCESS":
				checks = "passing"
			case "FAILURE", "ERROR":
				checks = "failing"
			case "PENDING", "EXPECTED":
				// GitHub waits for every check before calling the rollup
				// failed; one that already failed is failing now.
				checks = "running"
				if rollup.failed() {
					checks = "failing"
				}
			}
		}
	}
	switch {
	case pr.IsDraft:
		merge = "draft"
	case pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY":
		merge = "conflict"
	case pr.MergeStateStatus == "CLEAN" || pr.MergeStateStatus == "HAS_HOOKS":
		merge = "ready"
	case pr.MergeStateStatus == "BLOCKED" || pr.MergeStateStatus == "BEHIND" || pr.MergeStateStatus == "UNSTABLE":
		merge = "blocked"
	}
	if pr.ReviewThreads != nil {
		count := 0
		for _, node := range pr.ReviewThreads.Nodes {
			if !node.IsResolved {
				count++
			}
		}
		unresolved = &count
		capped = pr.ReviewThreads.PageInfo.HasNextPage
	}
	return
}

// inboxFields are what discovery reads of each pull request.
const inboxFields = `fragment Inbox on PullRequest {
 number state title headRefOid headRefName baseRefName isDraft additions deletions changedFiles updatedAt author { __typename login }
 headRepository { databaseId nameWithOwner } baseRepository { databaseId nameWithOwner }
 assignees(first: 20) { nodes { login } }
 reviewRequests(first: 100) { nodes { requestedReviewer { __typename ... on User { login } ... on Team { databaseId } } } pageInfo { hasNextPage } }
 reviewDecision latestOpinionatedReviews(first: 100) { nodes { state } pageInfo { hasNextPage } }
 latestReviews: reviews(last: 100) { nodes { state } pageInfo { hasPreviousPage } }
 commits(last: 1) { nodes { commit { statusCheckRollup { state contexts(first: 1) { checkRunCountsByState { state count } statusContextCountsByState { state count } } } } } }
 mergeable mergeStateStatus
 reviewThreads(first: 100) { nodes { isResolved } pageInfo { hasNextPage } }
}`

// sweepQuery reads every open pull request, the most recently updated first,
// so the pages a poll gets before it runs out of time are the liveliest, and
// the latest update of a pull request of any state, where a sweep's window
// begins.
const sweepQuery = `query LocalInbox($owner: String!, $name: String!, $cursor: String, $first: Int!) {
 viewer { login }
 rateLimit { cost remaining resetAt }
 repository(owner: $owner, name: $name) {
  viewerPermission
  latest: pullRequests(orderBy: {field: UPDATED_AT, direction: DESC}, first: 1) { nodes { updatedAt } }
  pullRequests(states: OPEN, orderBy: {field: UPDATED_AT, direction: DESC}, first: $first, after: $cursor) {
   pageInfo { hasNextPage endCursor }
   nodes { ...Inbox }
  }
 }
}
` + inboxFields

// windowQuery reads the pull requests of every state, the most recently
// updated first, with GitHub's count of open ones and the newest's number,
// which the cache must match.
const windowQuery = `query LocalInboxChanges($owner: String!, $name: String!, $cursor: String, $first: Int!) {
 viewer { login }
 rateLimit { cost remaining resetAt }
 repository(owner: $owner, name: $name) {
  viewerPermission
  open: pullRequests(states: OPEN) { totalCount }
  newest: pullRequests(states: OPEN, orderBy: {field: CREATED_AT, direction: DESC}, first: 1) { nodes { number } }
  pullRequests(orderBy: {field: UPDATED_AT, direction: DESC}, first: $first, after: $cursor) {
   pageInfo { hasNextPage endCursor }
   nodes { ...Inbox }
  }
 }
}
` + inboxFields

// numbersQuery reads pull requests by number, under the aliases p0, p1, ….
func numbersQuery(repository github.Repository, numbers []int) (string, map[string]any) {
	var parameters []string
	var fields strings.Builder
	variables := map[string]any{"owner": repository.Owner, "name": repository.Name}
	for i, number := range numbers {
		parameters = append(parameters, fmt.Sprintf("$n%d: Int!", i))
		fmt.Fprintf(&fields, "  p%d: pullRequest(number: $n%d) { ...Inbox }\n", i, i)
		variables[fmt.Sprintf("n%d", i)] = number
	}
	return "query LocalInboxPulls($owner: String!, $name: String!, " + strings.Join(parameters, ", ") + ") {\n viewer { login }\n rateLimit { cost remaining resetAt }\n repository(owner: $owner, name: $name) {\n  viewerPermission\n" + fields.String() + " }\n}\n" + inboxFields, variables
}

var errInvalid = errors.New("invalid GitHub discovery response")

type graphQLError struct {
	Type string
	Path []any
}

// graphQL sends one query on the shared GraphQL budget: it refuses while the
// budget is exhausted, records what the response says of the budget and turns
// a rate-limited response into errRateLimit. The caller judges the data and
// errors it returns.
func (p *poll) graphQL(ctx context.Context, query string, variables map[string]any) (json.RawMessage, []graphQLError, error) {
	if _, graph := p.budget.budgets(); rateDeadline(graph).After(p.now) {
		return nil, nil, errRateLimit
	}
	status, headers, body, err := p.reader.GraphQL(ctx, query, variables)
	if err != nil {
		return nil, nil, failed(ctx, err)
	}
	restartIdle(ctx)
	var response struct {
		Data   json.RawMessage
		Errors []graphQLError
	}
	parseErr := json.Unmarshal(body, &response)
	var budget struct {
		RateLimit *struct {
			Cost, Remaining int
			ResetAt         time.Time
		}
	}
	limited := status == 429 || status == 403 && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "")
	for _, issue := range response.Errors {
		limited = limited || issue.Type == "RATE_LIMITED"
	}
	p.budget.update(func(_, graph *RateLimit) {
		// GraphQL response headers describe its point budget, never the REST budget.
		var reset time.Time
		if n, e := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); e == nil {
			reset = time.Unix(n, 0)
		}
		if n, e := strconv.Atoi(headers.Get("X-RateLimit-Remaining")); e == nil {
			observe(graph, &n, reset)
		} else {
			observe(graph, nil, reset)
		}
		if json.Unmarshal(response.Data, &budget) == nil && budget.RateLimit != nil {
			rate := budget.RateLimit
			graph.Cost = &rate.Cost
			observe(graph, &rate.Remaining, rate.ResetAt)
		}
		if limited {
			graph.RetryAt = maxTime(graph.RetryAt, retryAt(p.now, headers))
		}
	})
	if limited {
		return nil, nil, errRateLimit
	}
	if status != 200 {
		return nil, nil, &statusError{status}
	}
	if parseErr != nil {
		return nil, nil, errInvalid
	}
	return response.Data, response.Errors, nil
}

// pullsPage is one page of a window or sweep.
type pullsPage struct {
	Viewer     actor
	Repository *struct {
		ViewerPermission string
		Open             *struct{ TotalCount int }
		Latest           *struct {
			Nodes []struct{ UpdatedAt time.Time }
		}
		Newest       *struct{ Nodes []struct{ Number int } }
		PullRequests struct {
			Nodes    []gqlPR
			PageInfo pageInfo
		}
	}
}

// page reads one page and checks every pull request on it before any is
// used, so a page is kept whole or not at all.
func (p *poll) page(ctx context.Context, repository github.Repository, query string, cursor any) (pullsPage, error) {
	data, issues, err := p.shrinking(ctx, func(size int) (json.RawMessage, []graphQLError, error) {
		return p.graphQL(ctx, query, map[string]any{"owner": repository.Owner, "name": repository.Name, "cursor": cursor, "first": size})
	})
	if err != nil {
		return pullsPage{}, err
	}
	var response pullsPage
	if len(issues) > 0 || json.Unmarshal(data, &response) != nil || response.Repository == nil {
		return pullsPage{}, errInvalid
	}
	for _, row := range response.Repository.PullRequests.Nodes {
		if err := valid(row); err != nil {
			return pullsPage{}, err
		}
	}
	p.identify(response.Viewer, response.Repository.ViewerPermission)
	return response, nil
}

// shrinking sends a query for up to p.size pull requests. A gateway error,
// GitHub giving up on a query that took too long, asks again for half as many,
// and the smaller size stays for the rest of the poll.
func (p *poll) shrinking(ctx context.Context, ask func(size int) (json.RawMessage, []graphQLError, error)) (json.RawMessage, []graphQLError, error) {
	if p.size == 0 {
		p.size = inboxPageSize
	}
	for attempt := 1; ; attempt++ {
		data, issues, err := ask(p.size)
		var status *statusError
		if !errors.As(err, &status) || !status.gateway() || attempt == inboxPageAttempts {
			return data, issues, err
		}
		p.size = max(p.size/2, inboxMinPageSize)
		timer := time.NewTimer(time.Duration(attempt) * inboxRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, err
		case <-timer.C:
		}
	}
}

func (p *poll) identify(viewer actor, permission string) {
	p.viewer, p.permission, p.identified = viewer, permission, true
}

// valid refuses a pull request discovery cannot list: an open one without a
// head, or an open one whose review requests did not fit, which could conceal
// automatic eligibility.
func valid(row gqlPR) error {
	if row.Number <= 0 || open(row) && row.HeadRefOid == "" {
		return errors.New("incomplete GitHub pull request")
	}
	if open(row) && row.ReviewRequests.PageInfo.HasNextPage {
		return errors.New("GitHub review requests exceeded limit")
	}
	return nil
}

// open is whether a row GitHub returned is an open pull request. A row of a
// query that only lists open ones may leave out its state.
func open(row gqlPR) bool { return row.State == "" || row.State == "OPEN" }

// apply merges one pull request GitHub described into the rows: an open one
// replaces what was cached, a closed or merged one leaves.
func (p *poll) apply(row gqlPR) {
	if at, ok := p.readAt[row.Number]; ok && row.UpdatedAt.Before(at) {
		return
	}
	p.readAt[row.Number] = row.UpdatedAt
	p.read[row.Number] = true
	if open(row) {
		p.rows[row.Number] = row
		delete(p.removed, row.Number)
		return
	}
	p.gone(row.Number)
	if row.State == "MERGED" && row.BaseRefName != "" {
		p.merged[row.BaseRefName] = true
	}
}

func (p *poll) gone(number int) {
	p.read[number] = true
	delete(p.rows, number)
	p.removed[number] = true
}

// refresh brings the rows up to date. With a cursor it reads the window of
// pull requests updated since, then re-reads the ones GitHub's update time
// does not follow (watch). It sweeps every open pull request when there is no
// complete cache yet, a sweep is due or under way, or the window's count or
// newest pull request disagrees with the rows. Rows read before an error are
// kept; nothing is removed for not being seen, and the cursor moves only once
// its whole window was read.
func (p *poll) refresh(ctx context.Context, repository github.Repository) error {
	p.rows = map[int]gqlPR{}
	for _, row := range p.cache.Pulls {
		p.rows[row.Number] = row
	}
	p.read, p.removed, p.merged, p.readAt = map[int]bool{}, map[int]bool{}, map[string]bool{}, map[int]time.Time{}
	if !p.cache.Complete {
		p.legacy = map[int]PullRequest{}
		for _, pr := range p.cache.Snapshot.PullRequests {
			p.legacy[pr.Number] = pr
		}
	}
	sweeping := !p.cache.Complete || p.cache.Sweep != nil || p.cache.Cursor.IsZero() || !p.now.Before(p.cache.Swept.Add(sweepInterval)) || p.now.Before(p.cache.Swept)
	// A first sweep that a poll left unfinished has no cursor yet: the window
	// goes back to when it began, so what changed since is not missed.
	base := p.cache.Cursor
	if base.IsZero() && p.cache.Sweep != nil {
		base = p.cache.Sweep.Floor
	}
	if !base.IsZero() {
		switch err := p.window(ctx, repository, base); {
		case errors.Is(err, errLongWindow):
			// Start a sweep afresh: its window begins at GitHub's latest
			// update, and verifying what it did not see drops what closed.
			p.cache.Cursor, p.cache.Sweep, p.open, p.newest = time.Time{}, nil, nil, nil
			return p.sweep(ctx, repository)
		case err != nil:
			return err
		}
	}
	if !sweeping {
		if err := p.numbers(ctx, repository, p.watch()); err != nil {
			return err
		}
		sweeping = p.drifted()
	}
	// The cursor moves once the window and the re-reads it called for were
	// read: a pull request merged in the window is not passed over before the
	// pull requests on its base are read again.
	p.cache.Cursor = maxTime(p.cache.Cursor, p.windowCursor)
	if sweeping {
		return p.sweep(ctx, repository)
	}
	return nil
}

// drifted is whether the rows disagree with what the window said of the open
// pull requests: one deleted, or a change the window could not show.
func (p *poll) drifted() bool {
	if p.open != nil && *p.open != len(p.rows) {
		return true
	}
	if p.newest != nil {
		newest := 0
		for number := range p.rows {
			newest = max(newest, number)
		}
		return *p.newest != newest
	}
	return false
}

// window reads the pull requests updated since base, newest first, down to
// the first page that reaches below base less the overlap. Only once it has
// read them all does the cursor move, to the newest update it saw.
func (p *poll) window(ctx context.Context, repository github.Repository, base time.Time) error {
	threshold := base.Add(-cursorOverlap)
	newest := base
	var cursor any
	for page := 0; ; page++ {
		if page >= windowPageLimit {
			return errLongWindow
		}
		response, err := p.page(ctx, repository, windowQuery, cursor)
		if err != nil {
			return err
		}
		found := response.Repository
		if page == 0 {
			if found.Open != nil {
				p.open = new(found.Open.TotalCount)
			}
			if found.Newest != nil {
				p.newest = new(0)
				if len(found.Newest.Nodes) > 0 {
					*p.newest = found.Newest.Nodes[0].Number
				}
			}
		}
		nodes := found.PullRequests.Nodes
		for _, row := range nodes {
			newest = maxTime(newest, row.UpdatedAt)
			p.apply(row)
		}
		info := found.PullRequests.PageInfo
		if !info.HasNextPage || len(nodes) == 0 || nodes[len(nodes)-1].UpdatedAt.Before(threshold) {
			p.windowCursor = newest
			return nil
		}
		if info.EndCursor == "" || info.EndCursor == cursor {
			return errors.New("invalid GitHub pagination cursor")
		}
		cursor = info.EndCursor
	}
}

// checksRunning is whether a pull request's head still has checks running. It
// reads the raw rollup, not the shown state: one already failing may still
// have checks running, and a retry can turn it passing.
func checksRunning(row gqlPR) bool {
	if row.Commits == nil || len(row.Commits.Nodes) == 0 {
		return false
	}
	rollup := row.Commits.Nodes[0].Commit.StatusCheckRollup
	return rollup != nil && (rollup.State == "PENDING" || rollup.State == "EXPECTED")
}

// errLongWindow is a window longer than windowPageLimit pages: the cursor is
// too old (after days asleep, say), and a sweep, which resumes when cut short,
// replaces it.
var errLongWindow = errors.New("GitHub discovery window too long")

// watch are the open pull requests this poll did not read whose state can
// change with no update GitHub reports: checks still running, a mergeability
// still being worked out while the last poll settles, and those on a base a
// pull request just merged into, which GitHub works out again once asked.
func (p *poll) watch() []int {
	type candidate struct {
		number, rank int
		at           time.Time
	}
	var candidates []candidate
	for number, row := range p.rows {
		if p.read[number] {
			continue
		}
		running := checksRunning(row)
		unknown := !row.IsDraft && (row.Mergeable == "UNKNOWN" || row.MergeStateStatus == "UNKNOWN")
		switch {
		case unknown && p.cache.Settling > 0, !row.IsDraft && p.merged[row.BaseRefName]:
			candidates = append(candidates, candidate{number, 0, row.UpdatedAt})
		case running:
			candidates = append(candidates, candidate{number, 1, row.UpdatedAt})
		}
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), b.at.Compare(a.at), cmp.Compare(a.number, b.number))
	})
	numbers := make([]int, 0, min(len(candidates), watchLimit))
	for _, c := range candidates[:min(len(candidates), watchLimit)] {
		numbers = append(numbers, c.number)
	}
	return numbers
}

// numbers re-reads pull requests by number, a page's worth to a query. One
// GitHub no longer has (deleted, or its number now an issue) is gone.
func (p *poll) numbers(ctx context.Context, repository github.Repository, numbers []int) error {
	for start := 0; start < len(numbers); {
		var chunk []int
		data, issues, err := p.shrinking(ctx, func(size int) (json.RawMessage, []graphQLError, error) {
			chunk = numbers[start:min(start+size, len(numbers))]
			query, variables := numbersQuery(repository, chunk)
			return p.graphQL(ctx, query, variables)
		})
		if err != nil {
			return err
		}
		start += len(chunk)
		missing := map[string]bool{}
		for _, issue := range issues {
			if issue.Type != "NOT_FOUND" || len(issue.Path) != 2 || issue.Path[0] != "repository" {
				return errInvalid
			}
			alias, _ := issue.Path[1].(string)
			missing[alias] = true
		}
		var response struct {
			Viewer     actor
			Repository map[string]json.RawMessage
		}
		if json.Unmarshal(data, &response) != nil || response.Repository == nil {
			return errInvalid
		}
		var permission string
		if raw, ok := response.Repository["viewerPermission"]; !ok || json.Unmarshal(raw, &permission) != nil {
			return errInvalid
		}
		rows := make([]*gqlPR, len(chunk))
		for i, number := range chunk {
			alias := fmt.Sprintf("p%d", i)
			raw, ok := response.Repository[alias]
			if !ok || string(raw) == "null" {
				if !missing[alias] && (!ok || len(issues) != 0) {
					return errInvalid
				}
				continue
			}
			var row gqlPR
			if json.Unmarshal(raw, &row) != nil || row.Number != number {
				return errInvalid
			}
			if err := valid(row); err != nil {
				return err
			}
			rows[i] = &row
		}
		p.identify(response.Viewer, permission)
		for i, row := range rows {
			if row == nil {
				p.gone(chunk[i])
			} else {
				p.apply(*row)
			}
		}
	}
	return nil
}

// sweep reads every open pull request, going on from where an earlier poll's
// sweep stopped. Once it has read them all, the cached pull requests it did
// not see are asked about by number, so one is dropped only
// when GitHub says it closed, merged or is gone, however the pages shifted.
func (p *poll) sweep(ctx context.Context, repository github.Repository) error {
	if p.cache.Sweep == nil {
		p.cache.Sweep = &sweep{}
	}
	progress := p.cache.Sweep
	seen := map[int]bool{}
	for _, number := range progress.Seen {
		seen[number] = true
	}
	var cursor any
	if progress.After != "" {
		cursor = progress.After
	}
	for page := 0; ; page++ {
		if page >= pageLimit {
			return errors.New("GitHub discovery pagination exceeded limit")
		}
		response, err := p.page(ctx, repository, sweepQuery, cursor)
		if err != nil {
			var status *statusError
			if cursor != nil && (errors.Is(err, errInvalid) || errors.As(err, &status) && !status.gateway()) {
				// GitHub may no longer accept the saved cursor: start again.
				// A gateway error is GitHub out of time, not refusing it.
				p.cache.Sweep = nil
			}
			return err
		}
		nodes := response.Repository.PullRequests.Nodes
		if latest := response.Repository.Latest; cursor == nil && latest != nil && len(latest.Nodes) > 0 {
			progress.Floor = latest.Nodes[0].UpdatedAt
		}
		for _, row := range nodes {
			p.apply(row)
			seen[row.Number] = true
		}
		progress.Seen = slices.Sorted(maps.Keys(seen))
		info := response.Repository.PullRequests.PageInfo
		if !info.HasNextPage {
			break
		}
		if info.EndCursor == "" || info.EndCursor == cursor {
			return errors.New("invalid GitHub pagination cursor")
		}
		cursor = info.EndCursor
		progress.After = info.EndCursor
	}
	var missing []int
	for number := range p.rows {
		if !seen[number] {
			missing = append(missing, number)
		}
	}
	for number := range p.legacy {
		if _, ok := p.rows[number]; !ok && !seen[number] && !p.removed[number] {
			missing = append(missing, number)
		}
	}
	slices.Sort(missing)
	if err := p.numbers(ctx, repository, missing); err != nil {
		return err
	}
	if p.cache.Cursor.IsZero() {
		// Every change since the sweep began is newer than its first page.
		p.cache.Cursor = progress.Floor
	}
	p.cache.Sweep, p.cache.Complete, p.cache.Swept, p.legacy = nil, true, p.now, nil
	return nil
}
