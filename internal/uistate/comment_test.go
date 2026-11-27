package uistate

import (
	"encoding/json"
	"errors"
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
	// A tile's comment names the tile, as a section's does.
	tile := strings.Replace(good, `"kind":"file","id":"a.go","title":"a.go"`, `"kind":"tile","id":"risks","title":"Risks"`, 1)
	if err := CheckReviewComment(json.RawMessage(tile)); err != nil {
		t.Fatalf("a tile target: %v", err)
	}
	if validate("review", []byte(`{"comments":[`+tile+`],"note":""}`)) != nil {
		t.Error("a record refuses a tile comment")
	}
	for _, tc := range []struct{ from, to, reason string }{
		{`"kind":"file","id":"a.go","title":"a.go"`, `"kind":"tile","id":"","title":"Risks"`, "names its id"},
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
