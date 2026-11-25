package decisionlog

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Opening the store drops the edit-overlap links earlier versions stored and
// keeps every link from a recorded creation, in every repository.
func TestOpenDropsInferredLinks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "decisions", "decisions.sqlite")
	s, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"o/r", "o/other"} {
		if err := s.ReplaceSessionCommits(ctx, repository, []string{"X", "Y"}, []SessionCommit{
			{Agent: "claude-code", SessionID: "creator", SHA: "X", Method: "commit_ref", Confidence: 0.95},
			{Agent: "claude-code", SessionID: "editor", SHA: "X", Method: inferredMethod, Confidence: 0.6},
			{Agent: "claude-code", SessionID: "editor", SHA: "Y", Method: inferredMethod, Confidence: 0.3},
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rows, err := s.db.QueryContext(ctx, `SELECT repository, sha, session_id, method FROM session_commit ORDER BY repository, sha, session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var repository, sha, session, method string
		if err := rows.Scan(&repository, &sha, &session, &method); err != nil {
			t.Fatal(err)
		}
		got = append(got, repository+" "+sha+" "+session+" "+method)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"o/other X creator commit_ref", "o/r X creator commit_ref"}
	if !slices.Equal(got, want) {
		t.Fatalf("links = %q, want %q", got, want)
	}
}
