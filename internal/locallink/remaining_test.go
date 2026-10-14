package locallink

import (
	"fmt"
	"slices"
	"testing"

	"github.com/baumai/baum/internal/locallink/linktest"
	"github.com/baumai/baum/linkage"
)

// A commit with no checkpoint goes through the same loop as the hosted
// resolver: after the best match, the matcher runs again over the other
// sessions while the commit's files are not all explained. Here the first
// commit changes nine files; one session edited five of them and another the
// other four, so each explains part and both are linked.
func TestResolveLinksEverySessionThatEditedPartOfACommit(t *testing.T) {
	const (
		most = "aaaaaaaa-0000-4000-8000-0000000000a1"
		rest = "aaaaaaaa-0000-4000-8000-0000000000a2"
	)
	files := make([]string, 0, 8)
	for i := range 8 {
		files = append(files, fmt.Sprintf("part/%d.txt", i))
	}
	f := linktest.NewWithFiles(t, files...).NoSession(t)
	f.AddEditor(t, most, "10", append([]string{"b.txt"}, files[:4]...)...)
	f.AddEditor(t, rest, "10", files[4:]...)
	result := resolve(t, request(f))
	got := linkedTo(result.Commits[0])
	if want := []string{most, rest}; len(got) != 1 || !slices.Equal(got["files_overlap"], want) {
		t.Fatalf("first commit links = %+v, want both editors by files_overlap", result.Commits[0].Links)
	}
	if len(result.Sessions) != 2 {
		t.Errorf("sessions = %+v, want both editors", result.Sessions)
	}
}

// Beside a confident link, a further heuristic match is kept when it is
// confident too and dropped when it is below the review confidence. The
// session that ran git commit names the first commit (commit_ref, 0.95) but
// edited nothing, so the matcher goes on to the sessions that edited b.txt.
func TestResolveKeepsOnlyConfidentMatchesBesideAConfidentOne(t *testing.T) {
	for _, tc := range []struct {
		name, hour string
		kept       bool
	}{
		// Finished a quarter of an hour before the commit.
		{"confident", "10", true},
		// Finished ten hours before: matched weakly, then dropped.
		{"weak", "00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const editor = "cccccccc-0000-4000-8000-0000000000c1"
			alone := linktest.New(t).NoSession(t)
			alone.AddEditor(t, editor, tc.hour, "b.txt")
			var confidence float64
			for _, link := range resolve(t, request(alone)).Commits[0].Links {
				if link.Session == editor {
					confidence = link.Confidence
				}
			}
			if confidence == 0 || (confidence >= linkage.ReviewConfidence) != tc.kept {
				t.Fatalf("on its own the editor matches with confidence %v; the case needs it %s", confidence, tc.name)
			}

			f := linktest.New(t)
			f.AddEditor(t, editor, tc.hour, "b.txt")
			got := linkedTo(resolve(t, request(f)).Commits[0])
			if !slices.Equal(got["commit_ref"], []string{linktest.SessionID}) {
				t.Fatalf("links = %+v, want the commit_ref", got)
			}
			if kept := slices.Contains(got["files_overlap"], editor); kept != tc.kept {
				t.Errorf("links = %+v, editor kept = %v, want %v", got, kept, tc.kept)
			}
		})
	}
}
