package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

type noRepositories struct{}

func (noRepositories) Repositories(context.Context) ([]repos.Repository, error) { return nil, nil }

// A client that reads the index on the event must not get the snapshot
// cached before the change.
func TestIndexChangedInvalidatesBeforePublishing(t *testing.T) {
	ctx := context.Background()
	index := reviewindex.NewCache(ctx, &reviewindex.Service{Registry: noRepositories{}})
	if _, err := index.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	before, ok := index.Fresh()
	if !ok {
		t.Fatal("no cached snapshot")
	}
	published := 0
	changed := indexChanged(index, func(kind string, _ any) error {
		if kind != "index" {
			t.Fatal(kind)
		}
		if _, ok := index.Fresh(); ok {
			t.Fatal("published before invalidating")
		}
		_, version, err := index.Current(ctx)
		if err != nil || version == before {
			t.Fatal("a reader on the event got the old snapshot", version, err)
		}
		published++
		return nil
	})
	changed()
	if published != 1 {
		t.Fatal("published", published)
	}
}

type pollIndexRegistry struct{}

func (pollIndexRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return []repos.Repository{{Repository: "acme/app"}}, nil
}

type pollIndexTransport struct{ title string }

func (p *pollIndexTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `[]`
	if r.URL.Path == "/user" {
		body = `{"login":"viewer"}`
	}
	if r.URL.Path == "/graphql" {
		data := map[string]any{"data": map[string]any{
			"viewer":    map[string]any{"login": "viewer"},
			"rateLimit": map[string]any{"cost": 1, "remaining": 4900, "resetAt": "2033-05-18T03:33:20Z"},
			"repository": map[string]any{"viewerPermission": "WRITE", "pullRequests": map[string]any{
				"nodes":    []any{map[string]any{"number": 1, "title": p.title, "state": "OPEN", "headRefOid": "head", "author": map[string]any{"login": "viewer"}, "baseRepository": map[string]any{"databaseId": 1, "nameWithOwner": "acme/app"}}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}},
		}}
		encoded, _ := json.Marshal(data)
		body = string(encoded)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// Exercise the actual successful discovery-poll -> cached index -> watcher
// comparison path, including a later real change to the same pull request.
func TestIndexWatchIgnoresUnchangedSuccessfulDiscoveryPoll(t *testing.T) {
	ctx := context.Background()
	transport := &pollIndexTransport{title: "A pull request"}
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: transport})
	discover := discovery.New(client, appdbtest.TempDir(t))
	index := reviewindex.NewCache(ctx, &reviewindex.Service{Registry: pollIndexRegistry{}, Discovery: discover})
	discover.Updated = index.Invalidate
	repository := github.Repository{Host: "github.com", Owner: "acme", Name: "app"}
	poll := func() reviewindex.Snapshot {
		t.Helper()
		if _, err := discover.PollNow(ctx, repository); err != nil {
			t.Fatal(err)
		}
		snapshot, err := index.Snapshot(ctx)
		if err != nil || len(snapshot.Entries) != 1 {
			t.Fatalf("index %+v: %v", snapshot, err)
		}
		return snapshot
	}
	value := func(snapshot reviewindex.Snapshot) string {
		b, err := json.Marshal(indexWatchValue(snapshot, nil))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := poll()
	second := poll()
	if first.Entries[0].MetadataFetchedAt == second.Entries[0].MetadataFetchedAt {
		t.Fatal("poll did not advance actual API timestamp")
	}
	if value(first) != value(second) {
		t.Fatal("unchanged successful poll changed notification content")
	}
	if second.Entries[0].MetadataFetchedAt == "" {
		t.Fatal("watcher mutated shared API snapshot")
	}
	transport.title = "A changed title"
	third := poll()
	if value(second) == value(third) {
		t.Fatal("real change did not change notification content")
	}
	withError, err := json.Marshal(indexWatchValue(third, map[string]string{"acme/app": "discovery_unavailable"}))
	if err != nil {
		t.Fatal(err)
	}
	if value(third) == string(withError) {
		t.Fatal("refresh health omitted")
	}
}
