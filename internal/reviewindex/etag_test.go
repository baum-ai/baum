package reviewindex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/store"
)

// detail=summary leaves an entry's latest version with the fields a list
// reads, and is cached and tagged apart from the full answer.
func TestSummaryDetailTrimsLatest(t *testing.T) {
	cost := 1.5
	c := testCache(func(context.Context, time.Time) (Snapshot, time.Time, error) {
		return Snapshot{Entries: []Entry{{
			PullRequest: discovery.PullRequest{Number: 1},
			Latest: &store.Metadata{Version: 2, Head: "abc", Backend: "claude", Model: "m", GeneratedAt: "t", RecordedCostUSD: &cost,
				Accounting: &agent.Accounting{ChargedTokens: 9}, PromptDigest: "digest", Session: &store.ReviewSession{ID: "s"}},
		}, {PullRequest: discovery.PullRequest{Number: 2}}}, Problems: []Problem{}}, time.Time{}, nil
	}, (&clock{now: time.Unix(1000, 0)}).Now)
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/index"+query, nil))
		return w
	}
	full, summary := get("?history=1"), get("?history=1&detail=summary")
	if !strings.Contains(full.Body.String(), "accounting") || !strings.Contains(full.Body.String(), "prompt_digest") {
		t.Fatal("the full answer lost fields", full.Body.String())
	}
	body := summary.Body.String()
	for _, gone := range []string{"accounting", "prompt_digest", `"session"`, "bundle_digest"} {
		if strings.Contains(body, gone) {
			t.Fatalf("summary still has %s: %s", gone, body)
		}
	}
	var got struct {
		Entries []struct {
			Latest *struct {
				Version         int      `json:"version"`
				Head            string   `json:"head"`
				Backend         string   `json:"backend"`
				Model           string   `json:"model"`
				GeneratedAt     string   `json:"generated_at"`
				RecordedCostUSD *float64 `json:"recorded_cost_usd"`
			} `json:"latest"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("%+v", got)
	}
	l := got.Entries[0].Latest
	if l == nil || l.Version != 2 || l.Head != "abc" || l.Backend != "claude" || l.Model != "m" || l.GeneratedAt != "t" || l.RecordedCostUSD == nil || *l.RecordedCostUSD != 1.5 || got.Entries[1].Latest != nil {
		t.Fatalf("%+v", got)
	}
	if summary.Header().Get("ETag") == "" || summary.Header().Get("ETag") == full.Header().Get("ETag") {
		t.Fatal("summary and full share a tag")
	}
}

// An unchanged cached snapshot is encoded once per history flag and answers
// 304 to its ETag; a new snapshot gets a new ETag.
func TestCacheHandlerRevalidates(t *testing.T) {
	number := 1
	c := testCache(func(context.Context, time.Time) (Snapshot, time.Time, error) {
		return Snapshot{Entries: []Entry{{PullRequest: discovery.PullRequest{Number: number}}}, Problems: []Problem{}}, time.Time{}, nil
	}, (&clock{now: time.Unix(1000, 0)}).Now)
	get := func(query, inm string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/index"+query, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		c.Handler().ServeHTTP(w, req)
		return w
	}
	first := get("", "")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("%d %v", first.Code, first.Header())
	}
	data := c.bodies[variant{}].body.Data
	if again := get("", ""); again.Header().Get("ETag") != etag || &c.bodies[variant{}].body.Data[0] != &data[0] {
		t.Fatal("unchanged snapshot was encoded again")
	}
	if w := get("", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	number = 2
	c.Invalidate()
	if w := get("", etag); w.Code != 200 || w.Header().Get("ETag") == etag {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}
