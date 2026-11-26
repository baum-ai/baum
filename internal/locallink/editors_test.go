package locallink

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// A session that edited a pull request's files but created none of its
// commits is not linked, however much it edited: a colleague's commits to the
// files a session here also edited used to link to it.
func TestResolveLeavesSessionsThatOnlyEditedUnlinked(t *testing.T) {
	const editor = "eeeeeeee-0000-4000-8000-0000000000e1"
	f := linktest.New(t)
	f.AddEditor(t, editor, "10", "b.txt", "c.txt")
	result := resolve(t, request(f))
	if got := linkedTo(result.Commits[0]); len(got) != 1 || !slices.Equal(got["commit_ref"], []string{linktest.SessionID}) {
		t.Fatalf("first commit links = %+v, want only its creator", result.Commits[0].Links)
	}
	if links := result.Commits[1].Links; len(links) != 0 {
		t.Fatalf("second commit links = %+v, want none: no session created it", links)
	}
	for _, s := range result.Sessions {
		if s.ID == editor {
			t.Fatalf("sessions = %+v, want the editor left out", result.Sessions)
		}
	}
	if _, ok := result.Graphs[editor]; ok {
		t.Fatal("the editor's transcript was exposed as linked")
	}
}
