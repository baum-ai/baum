package uiserver

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeMerger struct {
	merged []string
	err    error
}

func (f *fakeMerger) MergeOptions(context.Context, github.Repository, int) (github.MergeOptions, error) {
	return github.MergeOptions{Methods: []string{"MERGE", "SQUASH"}, Default: "SQUASH", State: "OPEN", HeadSHA: "abc"}, f.err
}

func (f *fakeMerger) MergePullRequest(_ context.Context, r github.Repository, number int, method, head string) error {
	if f.err != nil {
		return f.err
	}
	f.merged = append(f.merged, r.String()+" "+method+" "+head)
	return nil
}

func TestMergeHandler(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	merger := &fakeMerger{}
	var refreshed []string
	changed := 0
	h := MergeHandler(registry, merger, func(_ context.Context, r github.Repository) { refreshed = append(refreshed, r.String()) }, func() { changed++ })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/api/prs/merge-options?repository=ACME/bare&pr=7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"default":"SQUASH"`) || !strings.Contains(w.Body.String(), `"head_sha":"abc"`) {
		t.Fatalf("options: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/prs/merge-options?repository=acme/other&pr=7", ""); w.Code != 404 {
		t.Fatalf("unpicked options: %d", w.Code)
	}
	if w := do("GET", "/api/prs/merge-options?repository=acme/bare", ""); w.Code != 400 {
		t.Fatalf("missing pr: %d", w.Code)
	}
	for body, code := range map[string]int{
		`{"repository":"acme/other","pr":7,"method":"MERGE","head_sha":"abc"}`:             404,
		`{"repository":"acme/bare","pr":7,"method":"MERGE"}`:                               400,
		`{"repository":"acme/bare","pr":7,"method":"FAST_FORWARD","head_sha":"abc"}`:       400,
		`{"repository":"acme/bare","pr":7,"method":"MERGE","head_sha":"abc","extra":true}`: 400,
		`{"repository":"acme/bare","method":"MERGE","head_sha":"abc"}`:                     400,
	} {
		if w := do("POST", "/api/prs/merge", body); w.Code != code {
			t.Fatalf("%s: %d %s, want %d", body, w.Code, w.Body.String(), code)
		}
	}
	if len(merger.merged) != 0 || len(refreshed) != 0 {
		t.Fatalf("rejected requests merged: %v", merger.merged)
	}
	if w := do("POST", "/api/prs/merge", `{"repository":"ACME/bare","pr":7,"method":"SQUASH","head_sha":"abc"}`); w.Code != 200 || len(merger.merged) != 1 || merger.merged[0] != "acme/bare SQUASH abc" || len(refreshed) != 1 || changed != 1 {
		t.Fatalf("merge: %d %s %v", w.Code, w.Body.String(), merger.merged)
	}
	for _, c := range []struct {
		err  error
		code int
		body string
	}{
		{&github.MergeError{Code: github.MergeHeadMoved, Message: "moved"}, 409, `"code":"head_moved"`},
		{&github.MergeError{Code: github.MergeConflict, Message: "conflicts"}, 409, `"code":"conflict"`},
		{&github.MergeError{Code: github.MergeBlocked, Message: "protected"}, 422, `"message":"protected"`},
		{&github.MergeError{Code: github.MergeForbidden, Message: "no"}, 403, `"code":"forbidden"`},
		{context.DeadlineExceeded, 502, `"code":"failed"`},
	} {
		merger.err = c.err
		before := len(refreshed)
		w := do("POST", "/api/prs/merge", `{"repository":"acme/bare","pr":7,"method":"MERGE","head_sha":"abc"}`)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.body) || len(refreshed) != before {
			t.Fatalf("%v: %d %s", c.err, w.Code, w.Body.String())
		}
	}
}
