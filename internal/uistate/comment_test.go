package uistate

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// CheckReviewComment refuses what a review record refuses of one comment,
// saying why.
func TestCheckReviewComment(t *testing.T) {
	good := `{"id":"c1","target":{"kind":"file","id":"a.go","title":"a.go"},"body":"Why?","place":{"path":"a.go","line":4,"side":"RIGHT","start_line":2},"head":"abcdef1","created":1}`
	if err := CheckReviewComment(json.RawMessage(good)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, to, reason string }{
		{`"id":"c1"`, `"id":"no spaces"`, "id"},
		{`"body":"Why?"`, `"body":"  "`, "body"},
		{`"body":"Why?"`, `"body":"` + strings.Repeat("x", MaxCommentBody+1) + `"`, "body"},
		{`"kind":"file"`, `"kind":"line"`, "target"},
		{`"id":"a.go","title"`, `"id":"","title"`, "names its id"},
		{`"side":"RIGHT"`, `"side":"UP"`, "place"},
		{`"start_line":2`, `"start_line":4`, "place"},
		{`"head":"abcdef1"`, `"head":"not hex"`, "head"},
		{`"created":1`, `"created":-1`, "creation"},
	} {
		raw := strings.Replace(good, tc.from, tc.to, 1)
		err := CheckReviewComment(json.RawMessage(raw))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("%s: %v", tc.to, err)
		}
		// A record holding it is refused alike.
		if validate("review", []byte(`{"comments":[`+raw+`],"note":""}`)) == nil {
			t.Errorf("%s: a record holds it", tc.to)
		}
	}
	if validate("review", []byte(`{"comments":[`+good+`,`+good+`],"note":""}`)) == nil {
		t.Error("a record holds one id twice")
	}
	if validate("review", []byte(`{"comments":[`+good+`],"note":""}`)) != nil {
		t.Error("a record refuses a good comment")
	}
}

// AddReviewComment appends to what is there, keeping the note, the other
// comments and the sent reviews, and refuses what the record would not hold.
func TestAddReviewComment(t *testing.T) {
	s := store(t)
	const sent = `{"comments":[{"id":"c1","target":{"kind":"general","id":"","title":"The pull request"},"body":"First","place":null,"head":"","created":1}],"note":"Summary","sent":[{"url":"https://github.com/acme/demo/pull/7#pullrequestreview-1","event":"COMMENT","at":5,"note":"","comments":[]}]}`
	save(t, s, "review", "acme/demo#7", 0, "mutation-00000001", sent)
	start := 2
	id, record, err := s.AddReviewComment(ReviewKey("Acme/Demo", 7), ReviewComment{Target: &CommentTarget{Kind: "section", ID: "sec_retry", Title: "Retry"}, Body: "Why cap it?", Place: &CommentPlace{Path: "retry.go", Line: 4, Side: "RIGHT", StartLine: &start}, Head: "abcdef1"})
	if err != nil || id == "" || record.Revision != 2 {
		t.Fatalf("%q %+v %v", id, record, err)
	}
	var value struct {
		Comments []struct {
			ID     string        `json:"id"`
			Target CommentTarget `json:"target"`
			Body   string        `json:"body"`
		} `json:"comments"`
		Note string            `json:"note"`
		Sent []json.RawMessage `json:"sent"`
	}
	if err := json.Unmarshal(record.Value, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Comments) != 2 || value.Comments[0].ID != "c1" || value.Comments[1].ID != id || value.Comments[1].Target.ID != "sec_retry" || value.Comments[1].Body != "Why cap it?" || value.Note != "Summary" || len(value.Sent) != 1 {
		t.Fatalf("saved %s", record.Value)
	}
	// A pull request with no review yet gets one, the comment about it in
	// general.
	if _, record, err = s.AddReviewComment("acme/demo#8", ReviewComment{Body: "Looks right"}); err != nil || !strings.Contains(string(record.Value), `"kind":"general"`) || !strings.Contains(string(record.Value), `"note":""`) {
		t.Fatalf("%s %v", record.Value, err)
	}
	for _, c := range []ReviewComment{{Body: "  "}, {Body: "b", Target: &CommentTarget{Kind: "section"}}, {Body: "b", Place: &CommentPlace{Path: "a", Line: 1, Side: "UP"}}, {Body: "b", Head: "nothex"}} {
		if _, _, err := s.AddReviewComment("acme/demo#7", c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	for range MaxReviewComments - 2 {
		if _, _, err := s.AddReviewComment("acme/demo#7", ReviewComment{Body: "more"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddReviewComment("acme/demo#7", ReviewComment{Body: "one too many"}); !errors.Is(err, ErrReviewFull) {
		t.Fatalf("a full review: %v", err)
	}
}

// POST /api/ui-state/review/comments adds one comment and says so; it never
// answers the record.
func TestAddReviewCommentOverHTTP(t *testing.T) {
	s := store(t)
	h := s.Handler()
	call := func(body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/ui-state/review/comments", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if got := call(`{"repository":"acme/demo","pr":7,"body":"Why?"}`, 201); !strings.Contains(got, "pending review") || strings.Contains(got, "Why?") {
		t.Fatalf("answered %s", got)
	}
	if got := call(`{"repository":"acme/demo","pr":7,"body":"  "}`, 400); !strings.Contains(got, "body") {
		t.Fatalf("a blank body: %s", got)
	}
	for _, body := range []string{`{"repository":"acme/demo","body":"b"}`, `{"repository":"acme/demo","pr":7,"body":"b","id":"x"}`, `{"repository":"no-owner","pr":7,"body":"b"}`} {
		call(body, 400)
	}
	if record, _ := s.Read("review", "acme/demo#7"); record.Revision != 1 {
		t.Fatalf("revision %d", record.Revision)
	}
}
