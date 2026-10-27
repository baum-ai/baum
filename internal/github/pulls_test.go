package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listReader answers the pull request list from count pull requests,
// numbered from 1, each updated an hour before the one before it.
type listReader struct {
	count int
	etag  string
	// tooLarge refuses pages of more than this many as too large; 0 never.
	tooLarge int
	paths    []string
	etags    []string
}

func (r *listReader) Read(_ context.Context, path, etag string) (int, http.Header, []byte, error) {
	r.paths, r.etags = append(r.paths, path), append(r.etags, etag)
	if etag != "" && etag == r.etag {
		return http.StatusNotModified, http.Header{}, nil, nil
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return 0, nil, nil, err
	}
	perPage, _ := strconv.Atoi(parsed.Query().Get("per_page"))
	page, _ := strconv.Atoi(parsed.Query().Get("page"))
	if perPage < 1 || page < 1 {
		return 0, nil, nil, fmt.Errorf("bad path %s", path)
	}
	if r.tooLarge > 0 && perPage > r.tooLarge {
		return 0, nil, nil, ErrResponseTooLarge
	}
	rows := []map[string]any{}
	for n := (page-1)*perPage + 1; n <= min(page*perPage, r.count); n++ {
		user := map[string]any{"login": fmt.Sprintf("user%d", n), "type": "User"}
		if n == 2 {
			user = map[string]any{"login": "dependabot[bot]", "type": "Bot"}
		}
		rows = append(rows, map[string]any{
			"number": n, "title": fmt.Sprintf("PR %d", n), "body": nil, "state": "open", "draft": n == 3,
			"created_at": "2026-10-01T00:00:00Z", "updated_at": updated(n).Format(time.RFC3339), "user": user,
			"head": map[string]any{"ref": "feature", "sha": strings.Repeat("a", 40)},
			"base": map[string]any{"ref": "main", "sha": strings.Repeat("b", 40)},
		})
	}
	body, _ := json.Marshal(rows)
	return http.StatusOK, http.Header{"Etag": []string{r.etag}}, body, nil
}

// updated is pull request n's update time: higher numbers are older.
func updated(n int) time.Time {
	return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Add(-time.Duration(n) * time.Hour)
}

var widgets = Repository{Host: "github.com", Owner: "acme", Name: "widgets"}

func TestListPullRequests(t *testing.T) {
	reader := &listReader{count: pullsPerPage + 2, etag: `"v1"`}

	// Paging stops at the first pull request updated before since; one
	// updated exactly at since is listed.
	list, err := ListPullRequests(context.Background(), reader, widgets, "", updated(pullsPerPage+1))
	if err != nil {
		t.Fatal(err)
	}
	if list.Calls != 2 || len(list.Pulls) != pullsPerPage+1 || list.Pulls[pullsPerPage].Number != pullsPerPage+1 || list.ETag != `"v1"` || list.NotModified || list.Truncated {
		t.Fatalf("list = %d calls, %d pulls, etag %q", list.Calls, len(list.Pulls), list.ETag)
	}
	if !strings.HasPrefix(reader.paths[0], fmt.Sprintf("/repos/acme/widgets/pulls?state=all&sort=updated&direction=desc&per_page=%d&page=1", pullsPerPage)) {
		t.Fatalf("path = %s", reader.paths[0])
	}
	if reader.etags[1] != "" {
		t.Fatal("only the first page is read with the ETag")
	}
	bot, draft := list.Pulls[1], list.Pulls[2]
	if !bot.Bot || bot.Author != "dependabot[bot]" || list.Pulls[0].Bot || !draft.Draft || draft.HeadSHA != strings.Repeat("a", 40) || draft.BaseRef != "main" {
		t.Fatalf("pulls = %+v", list.Pulls[:3])
	}

	// An unchanged first page reads nothing more.
	list, err = ListPullRequests(context.Background(), reader, widgets, `"v1"`, time.Time{})
	if err != nil || !list.NotModified || list.Calls != 1 || len(list.Pulls) != 0 || list.ETag != `"v1"` {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

// A listing stops at its bound, and is truncated only when more were due.
func TestListPullRequestsTruncated(t *testing.T) {
	defer SetMaxPulls(2 * pullsPerPage)()
	list, err := ListPullRequests(context.Background(), &listReader{count: 3 * pullsPerPage}, widgets, "", time.Time{})
	if err != nil || !list.Truncated || list.Calls != 3 || len(list.Pulls) != maxPulls {
		t.Fatalf("list = %d calls, %d pulls, truncated %t, %v", list.Calls, len(list.Pulls), list.Truncated, err)
	}
	// Exactly the bound: nothing is left, so nothing was cut.
	list, err = ListPullRequests(context.Background(), &listReader{count: maxPulls}, widgets, "", time.Time{})
	if err != nil || list.Truncated || len(list.Pulls) != maxPulls {
		t.Fatalf("list = %d pulls, truncated %t, %v", len(list.Pulls), list.Truncated, err)
	}
	// More, but all older than since: not cut either.
	list, err = ListPullRequests(context.Background(), &listReader{count: 3 * pullsPerPage}, widgets, "", updated(maxPulls))
	if err != nil || list.Truncated {
		t.Fatalf("list truncated %t, %v", list.Truncated, err)
	}
}

// A page too large to read is read again at half the size, and the listing
// goes on at that size from where it was.
func TestListPullRequestsShrinksOversizedPages(t *testing.T) {
	reader := &listReader{count: 40, tooLarge: 16}
	list, err := ListPullRequests(context.Background(), reader, widgets, "", time.Time{})
	if err != nil || len(list.Pulls) != 40 || list.Pulls[39].Number != 40 {
		t.Fatalf("list = %d pulls, %v", len(list.Pulls), err)
	}
	if last := reader.paths[len(reader.paths)-1]; !strings.Contains(last, "per_page=16&page=3") {
		t.Fatalf("last path = %s", last)
	}
	// A first page read smaller keeps no ETag.
	list, err = ListPullRequests(context.Background(), &listReader{count: 40, tooLarge: 16, etag: `"v1"`}, widgets, "", time.Time{})
	if err != nil || list.ETag != "" {
		t.Fatalf("list = etag %q, %v", list.ETag, err)
	}
	if _, err := ListPullRequests(context.Background(), &listReader{count: 40, tooLarge: 4}, widgets, "", time.Time{}); err != ErrResponseTooLarge {
		t.Fatalf("err = %v, want too large below the floor", err)
	}
}
