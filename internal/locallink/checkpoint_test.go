package locallink

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/linkage"
	"github.com/baum-ai/baum/sessions/vendors"
)

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

// A trailer with no valid checkpoint id, or one naming a checkpoint the clone
// does not hold, leaves the commit to the heuristic steps.
func TestResolveFallsBackFromAnUnusableTrailer(t *testing.T) {
	for _, trailer := range []string{
		"Entire-Checkpoint: not-a-checkpoint",
		"Entire-Checkpoint: not-a-checkpoint\nEntire-Checkpoint: 01ARZ3NDEKTSV4RRFFQ69G5FAL",
		"Entire-Checkpoint: ffffffffffff",
		// A ULID is valid, but only the git-refs store holds those, and this
		// clone has only the checkpoint branch.
		"Entire-Checkpoint: 01ARZ3NDEKTSV4RRFFQ69G5FAV",
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

// A commit naming several checkpoints links to the union of the sessions they
// record, as the hosted resolver does. Repeats collapse, a checkpoint the
// clone does not hold adds nothing, and the id is matched in any case.
func TestResolveLinksTheUnionOfSeveralCheckpoints(t *testing.T) {
	const second = "0004a7a444a1"
	others := [2]string{"aaaaaaaa-1111-4000-8000-000000000001", "aaaaaaaa-1111-4000-8000-000000000002"}
	f := linktest.NewWithTrailer(t, strings.Join([]string{
		linktest.CheckpointTrailer,
		"Entire-Checkpoint: ffffffffffff",
		"entire-checkpoint: " + strings.ToUpper(second),
		linktest.CheckpointTrailer,
	}, "\n"))
	f.AddCheckpointEditing(t, "b.txt")
	f.AddCheckpointAt(t, second, others)
	result := resolve(t, request(f))
	if !result.Available {
		t.Fatalf("unavailable: %s: %s", result.Code, result.Reason)
	}
	want := append(slices.Clone(linktest.CheckpointSessions), others[:]...)
	slices.Sort(want)
	got := linkedTo(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["entire_checkpoint"], want) {
		t.Fatalf("first commit links = %+v, want both checkpoints' sessions", result.Commits[0].Links)
	}
}

// A malformed value beside a valid one is skipped with a warning; the valid
// checkpoint still links.
func TestResolveSkipsAMalformedValueBesideAValidOne(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Entire-Checkpoint: not-a-checkpoint\n"+linktest.CheckpointTrailer)
	f.AddCheckpointEditing(t, "b.txt")
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	result := resolve(t, req)
	got := linkedTo(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["entire_checkpoint"], linktest.CheckpointSessions) {
		t.Fatalf("first commit links = %+v, want the valid checkpoint's sessions", result.Commits[0].Links)
	}
	if !strings.Contains(warnings.String(), `skipping malformed Entire-Checkpoint value "not-a-checkpoint"`) {
		t.Errorf("warnings = %q, want the skipped value named", warnings.String())
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

// refSessions are the sessions the git-refs checkpoints below record, which
// are not CheckpointSessions, so a link to them cannot come from the branch.
var refSessions = [2]string{"bbbbbbbb-1111-4000-8000-000000000001", "bbbbbbbb-1111-4000-8000-000000000002"}

const (
	// ulidCheckpoint is only ever in the git-refs store.
	ulidCheckpoint = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	fetchHint      = "git fetch origin 'refs/entire/checkpoints/*:refs/entire/checkpoints/*'"
)

// A commit whose trailer names a checkpoint kept in a per-checkpoint ref links
// to its sessions, with a ULID id, in any case, or a 12-hex one, and with no
// checkpoint branch in the clone at all.
func TestResolveLinksACheckpointInTheGitRefsStore(t *testing.T) {
	for _, tc := range []struct{ name, id, trailer string }{
		{"ULID", ulidCheckpoint, "Entire-Checkpoint: " + ulidCheckpoint},
		{"lower-case ULID", ulidCheckpoint, "Entire-Checkpoint: " + strings.ToLower(ulidCheckpoint)},
		{"12-hex", linktest.CheckpointID, linktest.CheckpointTrailer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linktest.NewWithTrailer(t, tc.trailer)
			f.AddRefCheckpoint(t, tc.id, refSessions)
			var warnings bytes.Buffer
			req := request(f)
			req.Warn = &warnings
			result := resolve(t, req)
			if !result.Available {
				t.Fatalf("unavailable: %s: %s", result.Code, result.Reason)
			}
			got := linkedTo(result.Commits[0])
			if !slices.Equal(got["entire_checkpoint"], refSessions[:]) {
				t.Fatalf("first commit links = %+v, want the ref checkpoint's sessions", result.Commits[0].Links)
			}
			if strings.Contains(warnings.String(), "not in this clone") {
				t.Errorf("warnings = %q; the checkpoint is in the clone", warnings.String())
			}
			listed := map[string]bool{}
			for _, s := range result.Sessions {
				listed[s.ID] = true
			}
			for _, id := range refSessions {
				if !listed[id] {
					t.Errorf("linked session %s is not listed: %+v", id, result.Sessions)
				}
			}
		})
	}
}

// A migrated clone holds a 12-hex checkpoint in the branch and in a ref; the
// ref wins, and the other checkpoints in each store still link.
func TestResolveLinksAMigratedCloneFromTheRef(t *testing.T) {
	const branchOnly = "0004a7a444a1"
	branchOnlySessions := [2]string{"aaaaaaaa-1111-4000-8000-000000000001", "aaaaaaaa-1111-4000-8000-000000000002"}
	f := linktest.NewWithTrailer(t, strings.Join([]string{
		linktest.CheckpointTrailer,
		"Entire-Checkpoint: " + branchOnly,
		"Entire-Checkpoint: " + ulidCheckpoint,
	}, "\n"))
	f.AddCheckpoint(t)                                        // CheckpointID in the branch, with CheckpointSessions
	f.AddCheckpointAt(t, branchOnly, branchOnlySessions)      // only in the branch
	f.AddRefCheckpoint(t, linktest.CheckpointID, refSessions) // the migrated copy, with other sessions
	otherULID := [2]string{"cccccccc-1111-4000-8000-000000000001", "cccccccc-1111-4000-8000-000000000002"}
	f.AddRefCheckpoint(t, ulidCheckpoint, otherULID)
	result := resolve(t, request(f))
	want := append(append(slices.Clone(refSessions[:]), branchOnlySessions[:]...), otherULID[:]...)
	slices.Sort(want)
	if got := linkedTo(result.Commits[0]); !slices.Equal(got["entire_checkpoint"], want) {
		t.Fatalf("first commit links = %+v, want %v: the ref's sessions for the migrated id, not the branch copy's %v",
			result.Commits[0].Links, want, linktest.CheckpointSessions)
	}
}

// A ULID trailer whose ref is not in the clone leaves the commit to the
// heuristics, warns with the command that fetches the refs, and fetches
// nothing: origin is unreachable, and no checkpoint ref appears.
func TestResolveWarnsWithTheFetchHintForAMissingRefCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, linktest.Fixture)
	}{
		{"no checkpoints at all", func(*testing.T, linktest.Fixture) {}},
		{"only the branch", func(t *testing.T, f linktest.Fixture) { f.AddCheckpoint(t) }},
		{"another ref", func(t *testing.T, f linktest.Fixture) {
			f.AddRefCheckpoint(t, "01ARZ3NDEKTSV4RRFFQ69G5FAW", refSessions)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linktest.NewWithTrailer(t, "Entire-Checkpoint: "+ulidCheckpoint)
			tc.setup(t, f)
			var warnings bytes.Buffer
			req := request(f)
			req.Warn = &warnings
			result := resolve(t, req)
			got := linkedTo(result.Commits[0])
			if len(got) != 1 || !slices.Equal(got["commit_ref"], []string{linktest.SessionID}) {
				t.Errorf("first commit links = %+v, want the heuristic commit_ref", result.Commits[0].Links)
			}
			for _, want := range []string{ulidCheckpoint, "not in this clone", fetchHint} {
				if !strings.Contains(warnings.String(), want) {
					t.Errorf("warnings = %q, want it to contain %q", warnings.String(), want)
				}
			}
			if refs := linktest.Git(t, f.Repo, "for-each-ref", "refs/entire/checkpoints/"+ulidCheckpoint[len(ulidCheckpoint)-2:]+"/"+ulidCheckpoint); refs != "" {
				t.Errorf("the checkpoint ref appeared: %s", refs)
			}
		})
	}
}

// Reading a checkpoint ref in a partial clone downloads nothing either: the
// ref is there, its blobs are not, and the commit falls back to the heuristics.
func TestResolveReadsNoMissingRefCheckpointObject(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Entire-Checkpoint: "+ulidCheckpoint)
	f.AddRefCheckpoint(t, ulidCheckpoint, refSessions)
	linktest.Git(t, f.Repo, "config", "uploadpack.allowFilter", "true")
	linktest.Git(t, f.Repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	ref := "refs/entire/checkpoints/" + ulidCheckpoint[len(ulidCheckpoint)-2:] + "/" + ulidCheckpoint
	blob := linktest.Git(t, f.Repo, "rev-parse", ref+":metadata.json")
	partial := filepath.Join(t.TempDir(), "partial")
	linktest.Git(t, t.TempDir(), "clone", "-q", "--filter=blob:none", "file://"+filepath.ToSlash(f.Repo), partial)
	linktest.Git(t, partial, "fetch", "-q", "origin", "refs/entire/checkpoints/*:refs/entire/checkpoints/*")
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
