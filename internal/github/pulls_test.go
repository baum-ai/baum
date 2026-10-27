package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pagesReader answers the pull request list from pages of numbered pull
// requests, each updated an hour before the one before it.
type pagesReader struct {
	pages [][]int
	etag  string
	paths []string
	etags []string
}

func (r *pagesReader) Read(_ context.Context, path, etag string) (int, http.Header, []byte, error) {
	r.paths, r.etags = append(r.paths, path), append(r.etags, etag)
	if etag != "" && etag == r.etag {
		return http.StatusNotModified, http.Header{}, nil, nil
	}
	var page int
	if _, err := fmt.Sscanf(path[strings.LastIndex(path, "page=")+5:], "%d", &page); err != nil || page < 1 {
		return 0, nil, nil, fmt.Errorf("bad path %s", path)
	}
	rows := []map[string]any{}
	if page <= len(r.pages) {
		for _, n := range r.pages[page-1] {
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
	}
	body, _ := json.Marshal(rows)
	return http.StatusOK, http.Header{"Etag": []string{r.etag}}, body, nil
}

// updated is pull request n's update time: higher numbers are older.
func updated(n int) time.Time {
	return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).Add(-time.Duration(n) * time.Hour)
}

func TestListPullRequests(t *testing.T) {
	repo := Repository{Host: "github.com", Owner: "acme", Name: "widgets"}
	full := make([]int, pullsPerPage)
	for i := range full {
		full[i] = i + 1
	}
	reader := &pagesReader{pages: [][]int{full, {51, 52}}, etag: `"v1"`}

	// Paging stops at the first pull request updated before since; one
	// updated exactly at since is listed.
	list, err := ListPullRequests(context.Background(), reader, repo, "", updated(51))
	if err != nil {
		t.Fatal(err)
	}
	if list.Calls != 2 || len(list.Pulls) != 51 || list.Pulls[50].Number != 51 || list.ETag != `"v1"` || list.NotModified || list.Truncated {
		t.Fatalf("list = %d calls, %d pulls, etag %q", list.Calls, len(list.Pulls), list.ETag)
	}
	if !strings.HasPrefix(reader.paths[0], "/repos/acme/widgets/pulls?state=all&sort=updated&direction=desc&per_page=50&page=1") {
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
	list, err = ListPullRequests(context.Background(), reader, repo, `"v1"`, time.Time{})
	if err != nil || !list.NotModified || list.Calls != 1 || len(list.Pulls) != 0 || list.ETag != `"v1"` {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

// A listing that reaches its page bound says so.
func TestListPullRequestsTruncated(t *testing.T) {
	defer func(pages int) { maxPullPages = pages }(maxPullPages)
	maxPullPages = 1
	full := make([]int, pullsPerPage)
	for i := range full {
		full[i] = i + 1
	}
	list, err := ListPullRequests(context.Background(), &pagesReader{pages: [][]int{full, full}}, Repository{Host: "github.com", Owner: "acme", Name: "widgets"}, "", time.Time{})
	if err != nil || !list.Truncated || list.Calls != 1 || len(list.Pulls) != pullsPerPage {
		t.Fatalf("list = %+v, %v", list, err)
	}
}
