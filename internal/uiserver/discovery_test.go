package uiserver

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

type fakePRs struct{ polled []string }

func (f *fakePRs) Cached(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	return discovery.Snapshot{Repository: r.String(), PullRequests: []discovery.PullRequest{{Number: 7}}}, nil
}
func (f *fakePRs) Poll(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	f.polled = append(f.polled, r.String())
	return discovery.Snapshot{Repository: r.String()}, nil
}

func TestDiscoveryHandlerServesRepositoriesWithoutClones(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	prs := &fakePRs{}
	changed := 0
	h := DiscoveryHandler(registry, prs, func() { changed++ })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/api/prs", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"acme/bare"`) || !strings.Contains(w.Body.String(), `"number":7`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := do("POST", "/api/prs/refresh", `{"repository":"ACME/bare"}`); w.Code != 200 || len(prs.polled) != 1 || prs.polled[0] != "acme/bare" || changed != 1 {
		t.Fatalf("refresh: %d %s %v", w.Code, w.Body.String(), prs.polled)
	}
	for body, code := range map[string]int{
		`{"repository":"acme/bare","clone":"c"}`: 400,
		`{}`:                                     400,
		`{"repository":"acme/bare","x":1}`:       400,
		`{"repository":"acme/other"}`:            404,
		`{"clone":"nope"}`:                       404,
	} {
		if w := do("POST", "/api/prs/refresh", body); w.Code != code {
			t.Fatalf("%s: %d %s, want %d", body, w.Code, w.Body.String(), code)
		}
	}
	if len(prs.polled) != 1 {
		t.Fatalf("rejected requests polled: %v", prs.polled)
	}
}
