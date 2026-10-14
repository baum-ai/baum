package locallink

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baumai/baum/internal/locallink/linktest"
	"github.com/baumai/baum/linkage"
	"github.com/baumai/baum/sessions/vendors"
)

func TestEntireCheckpointID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trailers []string
		id       string
		present  bool
		invalid  bool
	}{
		{"none", []string{"Co-Authored-By: A <a@example.com>"}, "", false, false},
		{"one", []string{"Entire-Checkpoint: 0004a7a444a0"}, "0004a7a444a0", true, false},
		{"case", []string{"entire-checkpoint:  0004A7A444A0 "}, "0004a7a444a0", true, false},
		{"repeated", []string{"Entire-Checkpoint: 0004a7a444a0", "Entire-Checkpoint: 0004a7a444a0"}, "0004a7a444a0", true, false},
		{"malformed", []string{"Entire-Checkpoint: not-a-checkpoint"}, "", true, true},
		{"too long", []string{"Entire-Checkpoint: 0004a7a444a0ff"}, "", true, true},
		{"two ids", []string{"Entire-Checkpoint: 0004a7a444a0", "Entire-Checkpoint: 111111111111"}, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, present, err := entireCheckpointID(tc.trailers)
			if id != tc.id || present != tc.present || (err != nil) != tc.invalid {
				t.Errorf("= %q, %v, %v", id, present, err)
			}
			if err != nil && !errors.Is(err, errInvalidCheckpointTrailer) {
				t.Errorf("error %v is not errInvalidCheckpointTrailer", err)
			}
		})
	}
}

// linkedTo is the sessions a commit links to, by method.
func linkedTo(c Commit) map[string][]string {
	out := map[string][]string{}
	for _, link := range c.Links {
		out[link.Method] = append(out[link.Method], link.Session)
	}
	for _, sessions := range out {
		slices.Sort(sessions)
	}
	return out
}

// A commit whose trailer names a checkpoint on the clone's branch links to the
// sessions the checkpoint records. When they edited every file the commit
// changed, it links only to them, although the session that ran git commit
// would match it by commit_ref.
func TestResolveLinksACommitToItsCheckpointSessions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []vendors.Slug
	}{
		// Load reads the checkpoint branch and returns the sessions itself.
		{"loaded", nil},
		// Load reads no Entire session; the checkpoint is mapped for the link.
		{"mapped for the link", []vendors.Slug{vendors.ClaudeCode}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
			f.AddCheckpointEditing(t, "b.txt")
			req := request(f)
			req.Sessions.Sources = tc.sources
			result := resolve(t, req)
			if !result.Available {
				t.Fatalf("unavailable: %s: %s", result.Code, result.Reason)
			}
			first := result.Commits[0]
			want := map[string][]string{"entire_checkpoint": linktest.CheckpointSessions}
			if got := linkedTo(first); len(got) != 1 || !slices.Equal(got["entire_checkpoint"], want["entire_checkpoint"]) {
				t.Fatalf("first commit links = %+v, want only the checkpoint's sessions", first.Links)
			}
			for _, link := range first.Links {
				if link.Confidence != 1 {
					t.Errorf("checkpoint link confidence = %v", link.Confidence)
				}
			}
			listed := map[string]Session{}
			for _, s := range result.Sessions {
				listed[s.ID] = s
			}
			for _, id := range linktest.CheckpointSessions {
				s, ok := listed[id]
				if !ok {
					t.Errorf("linked session %s is not listed: %+v", id, result.Sessions)
					continue
				}
				if s.Source != "entire" || s.Vendor != "claude_code" {
					t.Errorf("session %s = %+v", id, s)
				}
			}
			if _, ok := listed[linktest.SessionID]; ok {
				t.Errorf("the heuristic match was listed beside the checkpoint's: %+v", result.Sessions)
			}
		})
	}
}

// When the checkpoint's sessions did not edit all of the commit's files, the
// heuristic steps go on over the other sessions until the files are covered,
// as in the hosted resolver. The checkpoint's sessions edited a.txt and the
// commit also changed b.txt: the session that ran git commit (which edited
// nothing) and a session that edited b.txt are linked beside them.
func TestResolveAddsMatchesBesideAnIncompleteCheckpoint(t *testing.T) {
	const editor = "bbbbbbbb-0000-4000-8000-00000000000b"
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
	f.AddCheckpointEditing(t, "a.txt")
	f.AddEditor(t, editor, "10", "b.txt")
	result := resolve(t, request(f))
	got := linkedTo(result.Commits[0])
	if len(got) != 3 || !slices.Equal(got["entire_checkpoint"], linktest.CheckpointSessions) ||
		!slices.Equal(got["commit_ref"], []string{linktest.SessionID}) || !slices.Equal(got["files_overlap"], []string{editor}) {
		t.Fatalf("first commit links = %+v, want the checkpoint's sessions, the commit_ref and the editor", result.Commits[0].Links)
	}
	listed := 0
	for _, s := range result.Sessions {
		if s.ID == editor || s.ID == linktest.SessionID {
			listed++
		}
	}
	if listed != 2 {
		t.Errorf("sessions = %+v, want the heuristic matches listed", result.Sessions)
	}
}

// A heuristic match below the review confidence is dropped once the commit
// has a confident link, which a checkpoint's always are.
func TestResolveDropsWeakMatchesBesideACheckpoint(t *testing.T) {
	const weak = "cccccccc-0000-4000-8000-00000000000c"
	// On its own, a session that edited b.txt but finished ten hours before
	// the first commit matches it weakly.
	plain := linktest.New(t).NoSession(t)
	plain.AddEditor(t, weak, "00", "b.txt")
	var confidence float64
	for _, link := range resolve(t, request(plain)).Commits[0].Links {
		if link.Session == weak {
			confidence = link.Confidence
		}
	}
	if confidence == 0 || confidence >= linkage.ReviewConfidence {
		t.Fatalf("the editor matches with confidence %v on its own; the test needs a weak match", confidence)
	}

	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
	f.AddCheckpoint(t)
	f.AddEditor(t, weak, "00", "b.txt")
	result := resolve(t, request(f))
	for _, link := range result.Commits[0].Links {
		if link.Session == weak {
			t.Errorf("the weak match was kept beside the checkpoint: %+v", result.Commits[0].Links)
		}
	}
	for _, s := range result.Sessions {
		if s.ID == weak {
			t.Errorf("the weak match was listed: %+v", result.Sessions)
		}
	}
}

// A trailer that is malformed, names two checkpoints, or names one the clone
// does not hold leaves the commit to the heuristic steps.
func TestResolveFallsBackFromAnUnusableTrailer(t *testing.T) {
	for _, trailer := range []string{
		"Entire-Checkpoint: not-a-checkpoint",
		"Entire-Checkpoint: " + linktest.CheckpointID + "\nEntire-Checkpoint: 111111111111",
		"Entire-Checkpoint: ffffffffffff",
	} {
		t.Run(trailer, func(t *testing.T) {
			f := linktest.NewWithTrailer(t, trailer)
			f.AddCheckpoint(t)
			result := resolve(t, request(f))
			got := linkedTo(result.Commits[0])
			if len(got) != 1 || !slices.Equal(got["commit_ref"], []string{linktest.SessionID}) {
				t.Errorf("first commit links = %+v, want the heuristic commit_ref", result.Commits[0].Links)
			}
		})
	}
}

// Reading the checkpoint a trailer names downloads nothing: in a partial clone
// whose promisor remote answers, the checkpoint's blobs stay absent and the
// commit falls back to the heuristics.
func TestResolveReadsNoMissingCheckpointObject(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
	f.AddCheckpoint(t)
	linktest.Git(t, f.Repo, "config", "uploadpack.allowFilter", "true")
	linktest.Git(t, f.Repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	blob := linktest.Git(t, f.Repo, "rev-parse", "entire/checkpoints/v1:"+linktest.CheckpointID[:2]+"/"+linktest.CheckpointID[2:]+"/metadata.json")
	partial := filepath.Join(t.TempDir(), "partial")
	linktest.Git(t, t.TempDir(), "clone", "-q", "--filter=blob:none", "file://"+filepath.ToSlash(f.Repo), partial)
	// origin stays reachable; another remote names the document's repository.
	linktest.Git(t, partial, "remote", "add", "upstream", linktest.UnreachableOrigin)
	if has(partial, blob) {
		t.Fatal("the checkpoint metadata is already in the partial clone")
	}
	req := request(f)
	req.Dir = partial
	result := resolve(t, req)
	if !result.Available {
		t.Fatalf("unavailable: %s: %s", result.Code, result.Reason)
	}
	if has(partial, blob) {
		t.Fatal("Resolve fetched the checkpoint metadata from the promisor remote")
	}
	if got := linkedTo(result.Commits[0]); len(got["entire_checkpoint"]) != 0 {
		t.Errorf("first commit links = %+v; the checkpoint is not readable here", result.Commits[0].Links)
	}
}

// has reports whether the repository holds the object, without letting git
// fetch it.
func has(dir, oid string) bool {
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", oid)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	return cmd.Run() == nil
}
