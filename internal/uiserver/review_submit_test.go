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

type fakeReviewer struct {
	repos       []string
	submissions []github.ReviewSubmission
	err         error
}

func (f *fakeReviewer) SubmitReview(_ context.Context, r github.Repository, number int, review github.ReviewSubmission) (github.ReviewResult, error) {
	if f.err != nil {
		return github.ReviewResult{}, f.err
	}
	f.repos = append(f.repos, r.String())
	f.submissions = append(f.submissions, review)
	return github.ReviewResult{URL: "https://github.com/acme/bare/pull/7#pullrequestreview-1"}, nil
}

func TestReviewHandler(t *testing.T) {
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "Acme/Bare", nil); err != nil {
		t.Fatal(err)
	}
	reviewer := &fakeReviewer{}
	changed := 0
	h := ReviewHandler(registry, reviewer, func() { changed++ })
	do := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "http://local/api/prs/review", strings.NewReader(body)))
		return w
	}
	comment := `{"path":"a.go","line":4,"body":"x"}`
	head := `"repository":"acme/bare","pr":7,"head_sha":"abc"`
	many := strings.TrimSuffix(strings.Repeat(comment+",", 101), ",")
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"unpicked":              {`{"repository":"acme/other","pr":7,"head_sha":"abc","event":"APPROVE"}`, 404},
		"missing pr":            {`{"repository":"acme/bare","head_sha":"abc","event":"APPROVE"}`, 400},
		"missing head":          {`{"repository":"acme/bare","pr":7,"event":"APPROVE"}`, 400},
		"bad event":             {`{` + head + `,"event":"MERGE"}`, 400},
		"unknown field":         {`{` + head + `,"event":"APPROVE","extra":1}`, 400},
		"two values":            {`{` + head + `,"event":"APPROVE"} {}`, 400},
		"not json":              {`nope`, 400},
		"comment needs content": {`{` + head + `,"event":"COMMENT"}`, 400},
		"changes needs content": {`{` + head + `,"event":"REQUEST_CHANGES"}`, 400},
		"too many comments":     {`{` + head + `,"event":"COMMENT","comments":[` + many + `]}`, 400},
		"no path":               {`{` + head + `,"event":"COMMENT","comments":[{"line":4,"body":"x"}]}`, 400},
		"no comment body":       {`{` + head + `,"event":"COMMENT","comments":[{"path":"a","line":4}]}`, 400},
		"line zero":             {`{` + head + `,"event":"COMMENT","comments":[{"path":"a","line":0,"body":"x"}]}`, 400},
		"bad side":              {`{` + head + `,"event":"COMMENT","comments":[{"path":"a","line":4,"side":"MIDDLE","body":"x"}]}`, 400},
		"start after line":      {`{` + head + `,"event":"COMMENT","comments":[{"path":"a","line":4,"start_line":4,"body":"x"}]}`, 400},
		"too large":             {`{` + head + `,"event":"COMMENT","body":"` + strings.Repeat("a", 300<<10) + `"}`, 400},
	} {
		if w := do(c.body); w.Code != c.code {
			t.Fatalf("%s: %d %s, want %d", name, w.Code, w.Body.String(), c.code)
		}
	}
	if len(reviewer.submissions) != 0 || changed != 0 {
		t.Fatalf("rejected requests submitted: %v", reviewer.submissions)
	}

	w := do(`{"repository":"ACME/bare","pr":7,"head_sha":"abc","event":"APPROVE"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"url":"https://github.com/acme/bare/pull/7#pullrequestreview-1"`) || changed != 1 || reviewer.repos[0] != "acme/bare" {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	w = do(`{` + head + `,"event":"REQUEST_CHANGES","body":"fix","comments":[{"path":"a.go","line":4,"body":"x"},{"path":"b.go","line":9,"side":"LEFT","start_line":7,"body":"y"}]}`)
	if w.Code != 200 || changed != 2 || len(reviewer.submissions) != 2 {
		t.Fatalf("with comments: %d %s", w.Code, w.Body.String())
	}
	got := reviewer.submissions[1]
	if got.CommitID != "abc" || got.Event != "REQUEST_CHANGES" || got.Body != "fix" || len(got.Comments) != 2 ||
		got.Comments[0].Side != "RIGHT" || got.Comments[0].StartLine != 0 ||
		got.Comments[1] != (github.ReviewComment{Path: "b.go", Line: 9, Side: "LEFT", StartLine: 7, StartSide: "LEFT", Body: "y"}) {
		t.Fatalf("submission: %+v", got)
	}
	if w = do(`{` + head + `,"event":"COMMENT","comments":[` + comment + `]}`); w.Code != 200 {
		t.Fatalf("comment-only: %d %s", w.Code, w.Body.String())
	}

	changed = 0
	for _, c := range []struct {
		err  error
		code int
		body string
	}{
		{&github.ReviewError{Code: github.ReviewForbidden, Message: "no"}, 403, `"code":"forbidden"`},
		{&github.ReviewError{Code: github.ReviewNotFound, Message: "gone"}, 404, `"code":"not_found"`},
		{&github.ReviewError{Code: github.ReviewOwnPull, Message: "yours"}, 422, `"code":"own_pull_request"`},
		{&github.ReviewError{Code: github.ReviewInvalidLine, Message: "lines"}, 422, `"message":"lines"`},
		{&github.ReviewError{Code: github.ReviewFailed, Message: "odd"}, 502, `"code":"failed"`},
		{context.DeadlineExceeded, 502, `"code":"failed"`},
	} {
		reviewer.err = c.err
		w := do(`{"repository":"acme/bare","pr":7,"head_sha":"abc","event":"APPROVE"}`)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.body) || changed != 0 {
			t.Fatalf("%v: %d %s", c.err, w.Code, w.Body.String())
		}
	}
}
