package reviewindex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
)

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
	data := c.bodies[false].body.Data
	if again := get("", ""); again.Header().Get("ETag") != etag || &c.bodies[false].body.Data[0] != &data[0] {
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
