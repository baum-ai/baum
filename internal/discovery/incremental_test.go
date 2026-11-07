package discovery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/github"
)

// hubPR is one pull request of the fake GitHub.
type hubPR struct {
	number                int
	state                 string
	title, author         string
	head, base            string
	updated               time.Time
	checks                string
	mergeable, mergeState string
	// threads is how many review threads are unresolved.
	threads int
	deleted bool
}

// hubQuery is one GraphQL query the fake answered or was asked.
type hubQuery struct {
	repo, name, cursor string
	numbers            []int
}

// hub is a fake GitHub that answers discovery's queries as GitHub does:
// pages ordered by update time, newest first, with keyset cursors, a window
// of any state, and pull requests by number with NOT_FOUND for gone ones.
type hub struct {
	mu        sync.Mutex
	repos     map[string]map[int]*hubPR
	pageSize  int
	queries   []hubQuery
	rest      []*http.Request
	remaining int
	// permission and teams answer with these statuses instead of 200.
	permission map[string]int
	teams      int
	// before, when set, may answer a query itself (a failure, a rate limit)
	// or wait; it runs without the hub's lock.
	before func(r *http.Request, q hubQuery) (*http.Response, error)
}

var hubReset = time.Date(2033, 5, 18, 3, 33, 20, 0, time.UTC)

func newHub() *hub {
	return &hub{repos: map[string]map[int]*hubPR{}, pageSize: 2, remaining: 4000, permission: map[string]int{}}
}

func (h *hub) add(repo string, pr *hubPR) *hubPR {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.repos[repo] == nil {
		h.repos[repo] = map[int]*hubPR{}
	}
	pr.state = cmp.Or(pr.state, "OPEN")
	pr.title = cmp.Or(pr.title, "PR "+strconv.Itoa(pr.number))
	pr.author = cmp.Or(pr.author, "writer")
	pr.head = cmp.Or(pr.head, "head"+strconv.Itoa(pr.number))
	pr.base = cmp.Or(pr.base, "main")
	pr.mergeable = cmp.Or(pr.mergeable, "MERGEABLE")
	pr.mergeState = cmp.Or(pr.mergeState, "CLEAN")
	h.repos[repo][pr.number] = pr
	return pr
}

// change edits a pull request under the hub's lock.
func (h *hub) change(repo string, number int, edit func(*hubPR)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	edit(h.repos[repo][number])
}

func (h *hub) asked() []hubQuery {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.queries)
}

func (h *hub) restAsked(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.rest {
		if strings.HasPrefix(r.URL.Path, prefix) {
			n++
		}
	}
	return n
}

func (h *hub) node(repo string, pr *hubPR) map[string]any {
	var rollup any
	if pr.checks != "" {
		rollup = map[string]any{"state": pr.checks}
	}
	owner := map[string]any{"databaseId": 1, "nameWithOwner": repo}
	return map[string]any{
		"number": pr.number, "state": pr.state, "title": pr.title, "headRefOid": pr.head, "headRefName": "branch" + strconv.Itoa(pr.number), "baseRefName": pr.base,
		"updatedAt": pr.updated.Format(time.RFC3339), "author": map[string]any{"__typename": "User", "login": pr.author},
		"headRepository": owner, "baseRepository": owner,
		"reviewRequests": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
		"commits":        map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}}}},
		"mergeable":      pr.mergeable, "mergeStateStatus": pr.mergeState,
		"reviewThreads": map[string]any{"nodes": slices.Repeat([]any{map[string]any{"isResolved": false}}, pr.threads), "pageInfo": map[string]any{"hasNextPage": false}},
	}
}

func (h *hub) roundTrip(r *http.Request) (*http.Response, error) {
	headers := http.Header{"X-Ratelimit-Reset": []string{strconv.FormatInt(hubReset.Unix(), 10)}}
	reply := func(status int, body any) (*http.Response, error) {
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
	}
	if r.Method != http.MethodPost {
		h.mu.Lock()
		h.rest = append(h.rest, r.Clone(r.Context()))
		headers.Set("X-Ratelimit-Remaining", strconv.Itoa(h.remaining))
		teams, permission := h.teams, h.permission
		h.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/user/teams") {
			if teams != 0 {
				return reply(teams, map[string]any{"message": "no"})
			}
			return reply(200, []any{map[string]any{"id": 42}})
		}
		login := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/collaborators/"):], "/collaborators/"), "/permission")
		if status := permission[login]; status != 0 {
			return reply(status, map[string]any{"message": "no"})
		}
		return reply(200, map[string]any{"permission": "write"})
	}
	var request struct {
		Query     string
		Variables map[string]any
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	q := hubQuery{repo: fmt.Sprint(request.Variables["owner"]) + "/" + fmt.Sprint(request.Variables["name"])}
	q.name = strings.TrimPrefix(request.Query[:strings.IndexAny(request.Query, "({")], "query ")
	if cursor, ok := request.Variables["cursor"].(string); ok {
		q.cursor = cursor
	}
	for i := 0; ; i++ {
		n, ok := request.Variables[fmt.Sprintf("n%d", i)].(float64)
		if !ok {
			break
		}
		q.numbers = append(q.numbers, int(n))
	}
	h.mu.Lock()
	h.queries = append(h.queries, q)
	before := h.before
	h.mu.Unlock()
	if before != nil {
		if response, err := before(r, q); response != nil || err != nil {
			return response, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	headers.Set("X-Ratelimit-Remaining", strconv.Itoa(h.remaining))
	data := map[string]any{"viewer": map[string]any{"login": "viewer"}, "rateLimit": map[string]any{"cost": 1, "remaining": h.remaining, "resetAt": hubReset.Format(time.RFC3339)}}
	repository := map[string]any{"viewerPermission": "WRITE"}
	data["repository"] = repository
	prs := h.repos[q.repo]
	var errs []any
	switch q.name {
	case "LocalInboxPulls":
		for i, number := range q.numbers {
			alias := fmt.Sprintf("p%d", i)
			if pr := prs[number]; pr != nil && !pr.deleted {
				repository[alias] = h.node(q.repo, pr)
				continue
			}
			repository[alias] = nil
			errs = append(errs, map[string]any{"type": "NOT_FOUND", "path": []any{"repository", alias}, "message": "Could not resolve"})
		}
	case "LocalInbox", "LocalInboxChanges":
		var list []*hubPR
		open, newest := 0, 0
		for _, pr := range prs {
			if pr.deleted {
				continue
			}
			if pr.state == "OPEN" {
				open++
				newest = max(newest, pr.number)
			}
			if q.name == "LocalInboxChanges" || pr.state == "OPEN" {
				list = append(list, pr)
			}
		}
		slices.SortFunc(list, func(a, b *hubPR) int { return cmp.Or(b.updated.Compare(a.updated), cmp.Compare(b.number, a.number)) })
		// A keyset cursor: the rows after the last one returned, in order.
		start := 0
		if q.cursor != "" {
			var at int64
			var number int
			if _, err := fmt.Sscanf(q.cursor, "k:%d:%d", &at, &number); err != nil {
				return reply(200, map[string]any{"errors": []any{map[string]any{"type": "INVALID_CURSOR"}}})
			}
			for start < len(list) && (list[start].updated.Unix() > at || list[start].updated.Unix() == at && list[start].number >= number) {
				start++
			}
		}
		end := min(start+h.pageSize, len(list))
		nodes := []any{}
		for _, pr := range list[start:end] {
			nodes = append(nodes, h.node(q.repo, pr))
		}
		endCursor := ""
		if end > start {
			last := list[end-1]
			endCursor = fmt.Sprintf("k:%d:%d", last.updated.Unix(), last.number)
		}
		repository["pullRequests"] = map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": end < len(list), "endCursor": endCursor}}
		if q.name == "LocalInboxChanges" {
			repository["open"] = map[string]any{"totalCount": open}
			newestNodes := []any{}
			if newest > 0 {
				newestNodes = append(newestNodes, map[string]any{"number": newest})
			}
			repository["newest"] = map[string]any{"nodes": newestNodes}
		}
	default:
		return reply(200, map[string]any{"errors": []any{map[string]any{"type": "UNKNOWN_QUERY"}}})
	}
	body := map[string]any{"data": data}
	if errs != nil {
		body["errors"] = errs
	}
	return reply(200, body)
}

var (
	appRepo   = github.Repository{Host: "github.com", Owner: "acme", Name: "app"}
	otherRepo = github.Repository{Host: "github.com", Owner: "acme", Name: "other"}
	hubStart  = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
)

func hubService(t *testing.T, h *hub) (*Service, *time.Time) {
	t.Helper()
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(h.roundTrip)})
	s := New(client, appdbtest.TempDir(t))
	now := hubStart
	var mu sync.Mutex
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	return s, &now
}

// minutes is hubStart moved by n minutes.
func minutes(n int) time.Time { return hubStart.Add(time.Duration(n) * time.Minute) }

func numbersOf(snapshot Snapshot) []int {
	var numbers []int
	for _, pr := range snapshot.PullRequests {
		numbers = append(numbers, pr.Number)
	}
	return numbers
}

func titleOf(snapshot Snapshot, number int) string {
	for _, pr := range snapshot.PullRequests {
		if pr.Number == number {
			return pr.Title
		}
	}
	return ""
}

func names(queries []hubQuery) []string {
	var out []string
	for _, q := range queries {
		out = append(out, q.name)
	}
	return out
}

func loadCache(t *testing.T, s *Service, repository github.Repository) diskCache {
	t.Helper()
	reader, err := s.client.REST(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := s.load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// After the first sweep a poll reads only the pull requests updated since
// the cursor, of any state: an edit, a new one and a closed one are merged
// into the cache, and the rest are neither read nor dropped.
func TestIncrementalPollReadsOnlyWhatChanged(t *testing.T) {
	h := newHub()
	for n := 1; n <= 7; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 10)})
	}
	s, now := hubService(t, h)
	first, err := s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(first), []int{1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("first poll %v %v", numbersOf(first), err)
	}
	if got := names(h.asked()); !slices.Equal(got, []string{"LocalInbox", "LocalInbox", "LocalInbox", "LocalInbox"}) {
		t.Fatalf("first poll asked %v, want a full sweep", got)
	}
	h.change("acme/app", 3, func(pr *hubPR) { pr.title, pr.updated = "Edited", minutes(1) })
	h.change("acme/app", 5, func(pr *hubPR) { pr.state, pr.updated = "CLOSED", minutes(1) })
	h.add("acme/app", &hubPR{number: 8, updated: minutes(1)})
	*now = minutes(2)
	before := len(h.asked())
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{1, 2, 3, 4, 6, 7, 8}) || titleOf(got, 3) != "Edited" {
		t.Fatalf("incremental poll %v %q %v", numbersOf(got), titleOf(got, 3), err)
	}
	// 8 and 5, then 3 and 7, then 6 and 4: 4 is older than the cursor (7's
	// update) less the overlap, so the window ends there.
	if asked := names(h.asked()[before:]); !slices.Equal(asked, []string{"LocalInboxChanges", "LocalInboxChanges", "LocalInboxChanges"}) {
		t.Fatalf("incremental poll asked %v", asked)
	}
	if cache := loadCache(t, s, appRepo); !cache.Cursor.Equal(minutes(1)) || !cache.Complete || cache.Sweep != nil {
		t.Fatalf("cursor %v complete %v sweep %+v", cache.Cursor, cache.Complete, cache.Sweep)
	}
	// The probe's fingerprint follows the merged cache, so it is not asked
	// to poll again for what this poll already read.
	if f := loadCache(t, s, appRepo).Fingerprint; f.Open != 7 || f.Newest != 8 || !f.Updated.Equal(minutes(1)) {
		t.Fatalf("fingerprint %+v", f)
	}
	// Once sweepInterval has passed a poll reads every open one again.
	*now = minutes(2).Add(sweepInterval)
	before = len(h.asked())
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	if asked := names(h.asked()[before:]); !slices.Contains(asked, "LocalInbox") {
		t.Fatalf("due sweep asked %v", asked)
	}
}

// A pull request updated in the cursor's second, or recorded a little after
// a later one, is still read: the window reaches below the cursor.
func TestCursorOverlapReadsTiesAndLateUpdates(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-10)})
	h.add("acme/app", &hubPR{number: 2, updated: minutes(-5)})
	s, now := hubService(t, h)
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	// 1 is updated in the very second the cursor holds; 3 opens with an
	// update time a minute before it.
	h.change("acme/app", 1, func(pr *hubPR) { pr.title, pr.updated = "Same second", minutes(-5) })
	h.add("acme/app", &hubPR{number: 3, title: "Late", updated: minutes(-6)})
	*now = minutes(1)
	before := len(h.asked())
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil || titleOf(got, 1) != "Same second" || titleOf(got, 3) != "Late" {
		t.Fatalf("poll %+v %v", got.PullRequests, err)
	}
	if asked := names(h.asked()[before:]); slices.Contains(asked, "LocalInbox") {
		t.Fatalf("asked %v, want the window alone", asked)
	}
}

// A window page that fails keeps the pages read before it, but the cursor
// stays where it was, so the next poll reads the whole window again.
func TestFailedWindowPageKeepsWhatItReadAndTheCursor(t *testing.T) {
	h := newHub()
	for n := 1; n <= 4; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 11)})
	}
	s, now := hubService(t, h)
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	cursor := loadCache(t, s, appRepo).Cursor
	for n := 1; n <= 4; n++ {
		h.change("acme/app", n, func(pr *hubPR) { pr.title, pr.updated = "New "+strconv.Itoa(n), minutes(n) })
	}
	fail := true
	h.before = func(r *http.Request, q hubQuery) (*http.Response, error) {
		if fail && q.name == "LocalInboxChanges" && q.cursor != "" {
			return &http.Response{StatusCode: 502, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		}
		return nil, nil
	}
	*now = minutes(5)
	got, err := s.PollNow(context.Background(), appRepo)
	if err == nil || !got.Stale || titleOf(got, 4) != "New 4" || titleOf(got, 3) != "New 3" || titleOf(got, 2) != "PR 2" || len(got.PullRequests) != 4 {
		t.Fatalf("partial poll %+v %v", got.PullRequests, err)
	}
	if cache := loadCache(t, s, appRepo); !cache.Cursor.Equal(cursor) {
		t.Fatalf("cursor moved to %v past what was not read (was %v)", cache.Cursor, cursor)
	}
	fail = false
	got, err = s.PollNow(context.Background(), appRepo)
	if err != nil || titleOf(got, 1) != "New 1" || titleOf(got, 2) != "New 2" {
		t.Fatalf("retry %+v %v", got.PullRequests, err)
	}
	if cache := loadCache(t, s, appRepo); !cache.Cursor.Equal(minutes(4)) {
		t.Fatalf("cursor %v", cache.Cursor)
	}
}

// A request that takes too long is a timeout, not a changed identity: the
// pages read before it are kept and shown, and the next poll goes on with the
// sweep from where it stopped rather than starting again.
func TestSlowSweepKeepsItsPagesAndResumes(t *testing.T) {
	for _, caller := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller deadline=%v", caller), func(t *testing.T) {
			h := newHub()
			for n := 1; n <= 5; n++ {
				h.add("acme/app", &hubPR{number: n, updated: minutes(n - 11)})
			}
			s, now := hubService(t, h)
			ctx := context.Background()
			if caller {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			} else {
				old := requestTimeout
				requestTimeout = 100 * time.Millisecond
				defer func() { requestTimeout = old }()
			}
			stall := true
			h.before = func(r *http.Request, q hubQuery) (*http.Response, error) {
				if stall && q.name == "LocalInbox" && q.cursor != "" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return nil, nil
			}
			got, err := s.PollNow(ctx, appRepo)
			if !errors.Is(err, ErrTimeout) || errors.Is(err, github.ErrIdentityChanged) {
				t.Fatalf("slow poll error %v, want a timeout", err)
			}
			if !got.Stale || !slices.Equal(numbersOf(got), []int{4, 5}) || got.Error != ErrTimeout.Error() {
				t.Fatalf("slow poll kept %v stale=%v error=%q", numbersOf(got), got.Stale, got.Error)
			}
			if cache := loadCache(t, s, appRepo); cache.Sweep == nil || cache.Sweep.After == "" || cache.Complete {
				t.Fatalf("sweep progress not kept: %+v complete=%v", cache.Sweep, cache.Complete)
			}
			stall = false
			*now = minutes(1)
			before := len(h.asked())
			got, err = s.PollNow(context.Background(), appRepo)
			if err != nil || !slices.Equal(numbersOf(got), []int{1, 2, 3, 4, 5}) {
				t.Fatalf("resumed poll %v %v", numbersOf(got), err)
			}
			for _, q := range h.asked()[before:] {
				if q.name == "LocalInbox" && q.cursor == "" {
					t.Fatalf("sweep started again: %+v", h.asked()[before:])
				}
			}
			if cache := loadCache(t, s, appRepo); cache.Sweep != nil || !cache.Complete {
				t.Fatalf("sweep not finished: %+v", cache.Sweep)
			}
		})
	}
}

// A sweep that spans polls reads the window of changes since it began on
// each, and asks about the cached pull requests it did not see before dropping
// any: one that moved past its cursor stays, one that closed leaves.
func TestResumedSweepDropsOnlyWhatGitHubSaysLeft(t *testing.T) {
	h := newHub()
	for n := 1; n <= 5; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 11)})
	}
	s, now := hubService(t, h)
	old := requestTimeout
	requestTimeout = 100 * time.Millisecond
	defer func() { requestTimeout = old }()
	stall := true
	h.before = func(r *http.Request, q hubQuery) (*http.Response, error) {
		if stall && q.name == "LocalInbox" && q.cursor != "" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return nil, nil
	}
	if _, err := s.PollNow(context.Background(), appRepo); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	// Seen: 5 and 4. Meanwhile 5 closes, 3 is edited (moving ahead of the
	// sweep's cursor) and 1 is deleted.
	h.change("acme/app", 5, func(pr *hubPR) { pr.state, pr.updated = "CLOSED", minutes(1) })
	h.change("acme/app", 3, func(pr *hubPR) { pr.title, pr.updated = "Moved", minutes(1) })
	h.change("acme/app", 1, func(pr *hubPR) { pr.deleted = true })
	stall = false
	*now = minutes(2)
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{2, 3, 4}) || titleOf(got, 3) != "Moved" {
		t.Fatalf("finished sweep %v %q %v", numbersOf(got), titleOf(got, 3), err)
	}
}

// A pull request GitHub deleted never appears in a window. The window's
// count of open pull requests disagrees with the cache, so the same poll
// sweeps, asks about it by number and drops it.
func TestDeletedPullRequestLeavesAtTheNextPoll(t *testing.T) {
	h := newHub()
	for n := 1; n <= 3; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 10)})
	}
	s, now := hubService(t, h)
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	h.change("acme/app", 2, func(pr *hubPR) { pr.deleted = true })
	*now = minutes(1)
	before := len(h.asked())
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{1, 3}) {
		t.Fatalf("poll %v %v", numbersOf(got), err)
	}
	asked := h.asked()[before:]
	verified := false
	for _, q := range asked {
		verified = verified || q.name == "LocalInboxPulls" && slices.Equal(q.numbers, []int{2})
	}
	if !verified {
		t.Fatalf("asked %+v, want 2 asked about by number", asked)
	}
}

// What GitHub's update time does not follow is read again by number on an
// incremental poll: running checks that finish, and the pull requests on a
// base another merged into, whose mergeability GitHub works out again.
func TestWatchedPullRequestsAreReadByNumber(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-30), checks: "PENDING", base: "dev"})
	h.add("acme/app", &hubPR{number: 2, updated: minutes(-30)})
	h.add("acme/app", &hubPR{number: 3, updated: minutes(-30), base: "release"})
	h.add("acme/app", &hubPR{number: 4, updated: minutes(-20)})
	s, now := hubService(t, h)
	first, err := s.PollNow(context.Background(), appRepo)
	if err != nil || first.PullRequests[0].Checks != "running" || first.PullRequests[1].Merge != "ready" {
		t.Fatalf("first %+v %v", first.PullRequests, err)
	}
	// The checks finish and 4 merges into main: GitHub works out 2's
	// mergeability again, and neither 1 nor 2 is updated.
	h.change("acme/app", 1, func(pr *hubPR) { pr.checks = "FAILURE" })
	h.change("acme/app", 2, func(pr *hubPR) { pr.mergeable, pr.mergeState = "UNKNOWN", "UNKNOWN" })
	h.change("acme/app", 4, func(pr *hubPR) { pr.state, pr.updated = "MERGED", minutes(1) })
	*now = minutes(2)
	before := len(h.asked())
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{1, 2, 3}) {
		t.Fatalf("poll %v %v", numbersOf(got), err)
	}
	var watched []int
	for _, q := range h.asked()[before:] {
		if q.name == "LocalInboxPulls" {
			watched = append(watched, q.numbers...)
		}
	}
	if !slices.Equal(watched, []int{2, 1}) {
		t.Fatalf("read by number %v, want 2 (base merged into) then 1 (checks running); 3 is on another base", watched)
	}
	if got.PullRequests[0].Checks != "failing" {
		t.Fatalf("checks %q", got.PullRequests[0].Checks)
	}
	// 2 keeps its merge state while GitHub works it out, and settles.
	if got.PullRequests[1].Merge != "ready" || !got.Settling || !got.NextPoll.Equal(minutes(2).Add(settleInterval)) {
		t.Fatalf("settling=%v next=%v merge=%q", got.Settling, got.NextPoll, got.PullRequests[1].Merge)
	}
	// The settle poll reads 2 again though nothing updated it.
	h.change("acme/app", 2, func(pr *hubPR) { pr.mergeable, pr.mergeState = "CONFLICTING", "DIRTY" })
	*now = got.NextPoll
	settled, err := s.Poll(context.Background(), appRepo)
	if err != nil || settled.PullRequests[1].Merge != "conflict" || settled.Settling {
		t.Fatalf("settle poll merge=%q settling=%v err=%v", settled.PullRequests[1].Merge, settled.Settling, err)
	}
}

// A cache of the older format keeps its inbox: a poll that fails still shows
// it, a sweep cut short replaces only what it read, and the first sweep that
// finishes asks about what it did not see before dropping it. No conditional
// response of the older format is reused.
func TestOlderCacheFormatKeepsTheInboxUntilASweep(t *testing.T) {
	h := newHub()
	for n := 1; n <= 3; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 10)})
	}
	s, now := hubService(t, h)
	reader, err := s.client.REST(context.Background(), appRepo)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.path(reader.CacheIdentity(), appRepo.String())
	old := diskCache{Format: 2, Interval: time.Minute, Snapshot: Snapshot{Repository: "acme/app", Viewer: "viewer", FetchedAt: minutes(-60), NextPoll: minutes(-59), PullRequests: []PullRequest{
		{Repository: "acme/app", Number: 1, Title: "Old 1", HeadSHA: "head1", State: "OPEN"},
		{Repository: "acme/app", Number: 99, Title: "Old 99", HeadSHA: "gone", State: "OPEN"},
	}}, Responses: map[string]cachedResponse{"/repos/acme/app/collaborators/writer/permission": {ETag: `"old"`, Body: json.RawMessage(`{"permission":"write"}`)}}}
	if err := s.save(path, old); err != nil {
		t.Fatal(err)
	}
	offline := true
	stall := false
	oldTimeout := requestTimeout
	requestTimeout = 100 * time.Millisecond
	defer func() { requestTimeout = oldTimeout }()
	h.before = func(r *http.Request, q hubQuery) (*http.Response, error) {
		if offline {
			return nil, errors.New("network unreachable")
		}
		if stall && q.name == "LocalInbox" && q.cursor != "" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return nil, nil
	}
	got, err := s.Poll(context.Background(), appRepo)
	if err == nil || !slices.Equal(numbersOf(got), []int{1, 99}) || titleOf(got, 1) != "Old 1" {
		t.Fatalf("offline upgrade %v %v", numbersOf(got), err)
	}
	offline, stall = false, true
	*now = minutes(1)
	got, err = s.PollNow(context.Background(), appRepo)
	if !errors.Is(err, ErrTimeout) || !slices.Equal(numbersOf(got), []int{1, 2, 3, 99}) || titleOf(got, 3) != "PR 3" || titleOf(got, 1) != "Old 1" {
		t.Fatalf("cut-short sweep %v %q %v", numbersOf(got), titleOf(got, 1), err)
	}
	stall = false
	*now = minutes(2)
	got, err = s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{1, 2, 3}) || titleOf(got, 1) != "PR 1" {
		t.Fatalf("finished sweep %v %v", numbersOf(got), err)
	}
	for _, r := range h.rest {
		if r.Header.Get("If-None-Match") != "" {
			t.Fatalf("older format's response reused for %s", r.URL)
		}
	}
	if cache := loadCache(t, s, appRepo); cache.Format != cacheFormat || !cache.Complete {
		t.Fatalf("format %d complete %v", cache.Format, cache.Complete)
	}
}

// One account's teams are read once per teamsInterval, whatever the number of
// repositories polled; a refusal is an answer kept as long.
func TestTeamsAreReadOncePerAccount(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%v", refused), func(t *testing.T) {
			h := newHub()
			if refused {
				h.teams = http.StatusForbidden
			}
			h.add("acme/app", &hubPR{number: 1, updated: minutes(-5)})
			h.add("acme/other", &hubPR{number: 1, updated: minutes(-5)})
			s, now := hubService(t, h)
			for _, repo := range []github.Repository{appRepo, otherRepo, appRepo} {
				got, err := s.PollNow(context.Background(), repo)
				if err != nil || got.PullRequests[0].TeamMembershipKnown == refused {
					t.Fatalf("%s: %+v %v", repo, got.PullRequests, err)
				}
			}
			if n := h.restAsked("/user/teams"); n != 1 {
				t.Fatalf("/user/teams read %d times, want once", n)
			}
			*now = hubStart.Add(teamsInterval)
			if _, err := s.PollNow(context.Background(), otherRepo); err != nil {
				t.Fatal(err)
			}
			if n := h.restAsked("/user/teams"); n != 2 {
				t.Fatalf("/user/teams read %d times after the interval, want twice", n)
			}
		})
	}
}

// An author's permission answer, a refusal included, is reused for
// permissionTTL; a failed request is not an answer and is asked again.
func TestPermissionAnswersAreReused(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, author: "writer", updated: minutes(-5)})
	h.add("acme/app", &hubPR{number: 2, author: "forbidden", updated: minutes(-5)})
	h.add("acme/app", &hubPR{number: 3, author: "missing", updated: minutes(-5)})
	h.add("acme/app", &hubPR{number: 4, author: "flaky", updated: minutes(-5)})
	h.permission["forbidden"] = http.StatusForbidden
	h.permission["missing"] = http.StatusNotFound
	h.permission["flaky"] = http.StatusBadGateway
	s, now := hubService(t, h)
	got, err := s.PollNow(context.Background(), appRepo)
	if err != nil {
		t.Fatal(err)
	}
	if pr := got.PullRequests; pr[0].AuthorHasWrite == nil || !*pr[0].AuthorHasWrite || pr[1].AuthorHasWrite != nil || pr[2].AuthorHasWrite != nil || pr[3].AuthorHasWrite != nil {
		t.Fatalf("permissions %+v", pr)
	}
	if n := h.restAsked("/repos/acme/app/collaborators/"); n != 4 {
		t.Fatalf("asked %d permissions, want 4", n)
	}
	h.permission["flaky"] = 0
	*now = minutes(1)
	got, err = s.PollNow(context.Background(), appRepo)
	if err != nil || got.PullRequests[3].AuthorHasWrite == nil {
		t.Fatalf("flaky author %+v %v", got.PullRequests[3], err)
	}
	if n := h.restAsked("/repos/acme/app/collaborators/"); n != 5 {
		t.Fatalf("asked %d permissions, want only the failed one again", n)
	}
	*now = hubStart.Add(permissionTTL)
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	if n := h.restAsked("/repos/acme/app/collaborators/"); n != 8 {
		t.Fatalf("asked %d permissions, want the three answers from the first poll asked again", n)
	}
}

// Polls of different repositories run at once: neither waits for the other's
// GitHub requests, nor do Merged and Observe on another repository. A rate
// limit one of them meets stops the other's next request, and a response that
// left before it cannot restore the budget.
func TestConcurrentPollsShareTheRatePause(t *testing.T) {
	h := newHub()
	for n := 1; n <= 3; n++ {
		h.add("acme/app", &hubPR{number: n, updated: minutes(n - 10)})
		h.add("acme/other", &hubPR{number: n, updated: minutes(n - 10)})
	}
	s, _ := hubService(t, h)
	appWaits := make(chan struct{})
	limited := make(chan struct{})
	h.before = func(r *http.Request, q hubQuery) (*http.Response, error) {
		switch {
		case q.repo == "acme/app" && q.cursor == "":
			// app's first page is in flight while other polls and meets the
			// limit; it answers afterwards with a budget left.
			close(appWaits)
			<-limited
		case q.repo == "acme/other":
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"errors":[{"type":"RATE_LIMITED"}]}`)), Request: r}, nil
		}
		return nil, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.PollNow(context.Background(), appRepo); done <- err }()
	<-appWaits
	// The other repository's poll does not wait behind app's request.
	other := make(chan error, 1)
	go func() { _, err := s.PollNow(context.Background(), otherRepo); other <- err }()
	select {
	case err := <-other:
		if !errors.Is(err, errRateLimit) {
			t.Fatalf("other poll %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a poll of another repository waited behind app's")
	}
	if err := s.Merged(context.Background(), otherRepo, 1, "head1"); err != nil {
		t.Fatal(err)
	}
	close(limited)
	if err := <-done; !errors.Is(err, errRateLimit) {
		t.Fatalf("app poll went on past the shared limit: %v", err)
	}
	for _, q := range h.asked() {
		if q.repo == "acme/app" && q.cursor != "" {
			t.Fatalf("app asked for another page after the limit: %+v", h.asked())
		}
	}
}

// observe keeps the lower remaining count within one window.
func TestObserveKeepsTheLowerCountOfAWindow(t *testing.T) {
	var rate RateLimit
	observe(&rate, new(10), hubReset)
	observe(&rate, new(0), hubReset)
	observe(&rate, new(7), hubReset)
	if *rate.Remaining != 0 {
		t.Fatalf("remaining %d", *rate.Remaining)
	}
	observe(&rate, new(4999), hubReset.Add(time.Hour))
	if *rate.Remaining != 4999 || !rate.Reset.Equal(hubReset.Add(time.Hour)) {
		t.Fatalf("new window %+v", rate)
	}
	observe(&rate, new(1), hubReset)
	if *rate.Remaining != 4999 {
		t.Fatalf("an earlier window's reading replaced the current: %d", *rate.Remaining)
	}
}

// What the app records between polls holds across an incremental poll that
// does not read the pull request again: a merge from the app, even when the
// next poll fails, and a thread count GitHub's update time does not follow.
func TestAppRecordsHoldAcrossIncrementalPolls(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-25), base: "dev", threads: 2})
	h.add("acme/app", &hubPR{number: 2, updated: minutes(-20)})
	h.add("acme/app", &hubPR{number: 3, updated: minutes(-15)})
	s, now := hubService(t, h)
	if _, err := s.PollNow(context.Background(), appRepo); err != nil {
		t.Fatal(err)
	}
	if err := s.Merged(context.Background(), appRepo, 3, "head3"); err != nil {
		t.Fatal(err)
	}
	h.change("acme/app", 3, func(pr *hubPR) { pr.state, pr.updated = "MERGED", minutes(0) })
	offline := true
	h.before = func(*http.Request, hubQuery) (*http.Response, error) {
		if offline {
			return nil, errors.New("network unreachable")
		}
		return nil, nil
	}
	*now = minutes(1)
	got, err := s.PollNow(context.Background(), appRepo)
	if err == nil || slices.Contains(numbersOf(got), 3) {
		t.Fatalf("failed poll listed the merged pull request again: %v %v", numbersOf(got), err)
	}
	offline = false
	// A thread of 1 is resolved: GitHub moves no update time.
	if moved, err := s.Unresolved(context.Background(), appRepo, 1, 1); err != nil || !moved {
		t.Fatalf("unresolved moved=%v %v", moved, err)
	}
	h.change("acme/app", 1, func(pr *hubPR) { pr.threads = 1 })
	*now = minutes(2)
	before := len(h.asked())
	got, err = s.PollNow(context.Background(), appRepo)
	if err != nil || !slices.Equal(numbersOf(got), []int{1, 2}) {
		t.Fatalf("poll %v %v", numbersOf(got), err)
	}
	for _, q := range h.asked()[before:] {
		if slices.Contains(q.numbers, 1) || q.name == "LocalInbox" {
			t.Fatalf("1 was read again (%+v); the test needs it left alone", q)
		}
	}
	if pr := got.PullRequests[0]; pr.Unresolved == nil || *pr.Unresolved != 1 {
		t.Fatalf("thread count %v, want the app's 1", pr.Unresolved)
	}
}
