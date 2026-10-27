package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

func TestProbeComparesWithTheLastCompletePoll(t *testing.T) {
	t.Setenv("GH_HOST", "github.com")
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-time.Hour)
	probe := ""
	var asked []string
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `[]`
		if r.Method == http.MethodPost {
			var request struct {
				Query     string
				Variables map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(request.Query, "query Probe(") {
				asked = asked[:0]
				for i := 0; request.Variables[fmt.Sprintf("o%d", i)] != ""; i++ {
					asked = append(asked, request.Variables[fmt.Sprintf("o%d", i)]+"/"+request.Variables[fmt.Sprintf("n%d", i)])
				}
				body = probe
			} else {
				nodes := []string{}
				for number, at := range map[int]time.Time{1: updated.Add(-time.Hour), 2: updated} {
					nodes = append(nodes, fmt.Sprintf(`{"number":%d,"title":"PR","headRefOid":"head%d","updatedAt":%q,"author":{"login":"viewer"},"headRepository":{"databaseId":1,"nameWithOwner":"acme/app"},"baseRepository":{"databaseId":1,"nameWithOwner":"acme/app"}}`, number, number, at.Format(time.RFC3339)))
				}
				body = `{"data":{"viewer":{"login":"viewer"},"repository":{"pullRequests":{"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false}}}}}`
			}
		} else if strings.Contains(r.URL.Path, "/permission") {
			body = `{"permission":"write"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	s := New(client, t.TempDir())
	s.now = func() time.Time { return now }
	app := github.Repository{Host: "github.com", Owner: "acme", Name: "app"}
	unpolled := github.Repository{Host: "github.com", Owner: "acme", Name: "unpolled"}
	first, err := s.Poll(context.Background(), app)
	if err != nil || len(first.PullRequests) != 2 {
		t.Fatalf("poll %+v %v", first, err)
	}
	answer := func(newest, open int, at time.Time) string {
		return fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":4990,"resetAt":%q},"r0":{"newest":{"totalCount":%d,"nodes":[{"number":%d}]},"recent":{"nodes":[{"updatedAt":%q}]}}}}`, now.Add(time.Hour).Format(time.RFC3339), open, newest, at.Format(time.RFC3339))
	}
	for _, tc := range []struct {
		name    string
		answer  string
		changed bool
	}{
		{"same", answer(2, 2, updated), false},
		{"opened", answer(3, 3, updated), true},
		{"closed", answer(2, 1, updated), true},
		{"pushed", answer(2, 2, updated.Add(time.Minute)), true},
		{"gone", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"]}]}`, false},
	} {
		probe = tc.answer
		changed, err := s.Probe(context.Background(), []github.Repository{app, unpolled})
		if err != nil || changed["acme/app"] != tc.changed || len(changed) > 1 {
			t.Fatalf("%s: changed=%v err=%v", tc.name, changed, err)
		}
		// A repository never polled is left to its scheduled poll.
		if len(asked) != 1 || asked[0] != "acme/app" {
			t.Fatalf("%s: asked %v", tc.name, asked)
		}
	}

	// A poll where only update times moved still backs off, and records the
	// new fingerprint.
	updated = updated.Add(time.Minute)
	now = first.NextPoll
	second, err := s.Poll(context.Background(), app)
	if err != nil || !second.NextPoll.Equal(now.Add(time.Minute)) {
		t.Fatalf("second poll next=%v err=%v", second.NextPoll, err)
	}
	probe = answer(2, 2, updated)
	if changed, err := s.Probe(context.Background(), []github.Repository{app}); err != nil || len(changed) != 0 {
		t.Fatalf("after poll: changed=%v err=%v", changed, err)
	}

	// A rate-limited probe records the budget, and the next one waits for it
	// without asking.
	probe = `{"errors":[{"type":"RATE_LIMITED"}]}`
	if _, err := s.Probe(context.Background(), []github.Repository{app}); err != errRateLimit {
		t.Fatalf("limited probe %v", err)
	}
	asked = nil
	if _, err := s.Probe(context.Background(), []github.Repository{app}); err != errRateLimit || asked != nil {
		t.Fatalf("budget not honored: %v asked=%v", err, asked)
	}
}
