package locallink

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/locallink/linktest"
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

// Concurrent sessions in a checkpoint are candidates, not automatic links.
func TestResolveCheckpointMembershipNeedsIndependentEvidence(t *testing.T) {
	for _, sources := range [][]vendors.Slug{nil, {vendors.ClaudeCode}} {
		f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
		f.AddCheckpointEditing(t, "b.txt")
		req := request(f)
		req.Sessions.Sources = sources
		result := resolve(t, req)
		first := result.Commits[0]
		if !slices.Equal(first.CheckpointSessions, linktest.CheckpointSessions) {
			t.Fatalf("checkpoint candidates = %v", first.CheckpointSessions)
		}
		got := linkedTo(first)
		if len(got) != 2 || !slices.Equal(got["commit_ref"], []string{linktest.SessionID}) ||
			!slices.Equal(got["files_overlap"], linktest.CheckpointSessions[:1]) {
			t.Fatalf("links = %+v; want independent creator and checkpoint editor only", first.Links)
		}
		for _, link := range first.Links {
			if link.Method == "entire_checkpoint" || link.Confidence == 1 {
				t.Fatalf("checkpoint membership became proof: %+v", link)
			}
		}
		for _, session := range result.Sessions {
			if session.ID == linktest.CheckpointSessions[1] {
				t.Fatal("unrelated checkpoint session was listed as linked")
			}
		}
		if _, ok := result.Graphs[linktest.CheckpointSessions[1]]; ok {
			t.Fatal("unrelated checkpoint session transcript was exposed as linked")
		}
	}
}

func TestResolveReadOnlyCheckpointDoesNotSuppressOutsideEditor(t *testing.T) {
	const editor = "bbbbbbbb-0000-4000-8000-00000000000b"
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer).NoSession(t)
	f.AddCheckpoint(t)
	f.AddEditor(t, editor, "10", "b.txt")
	result := resolve(t, request(f))
	got := linkedTo(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["files_overlap"], []string{editor}) {
		t.Fatalf("links = %+v; want the outside editor", result.Commits[0].Links)
	}
}

func TestResolveCheckpointAssociationAloneLeavesLinksEmpty(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer).NoSession(t)
	f.AddCheckpoint(t)
	result := resolve(t, request(f))
	if len(result.Commits[0].Links) != 0 || len(result.Sessions) != 0 || len(result.Graphs) != 0 {
		t.Fatalf("membership alone added attribution: %+v", result)
	}
	if !slices.Equal(result.Commits[0].CheckpointSessions, linktest.CheckpointSessions) {
		t.Fatal("checkpoint association was lost")
	}
}

func TestCheckpointDoesNotBreakATieWithAnOutsideEditor(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer).NoSession(t)
	f.AddCheckpointEditing(t, "b.txt")
	f.AddEditor(t, "bbbbbbbb-0000-4000-8000-00000000000b", "10", "b.txt")
	result := resolve(t, request(f))
	if len(result.Commits[0].Links) != 0 {
		t.Fatalf("checkpoint membership broke an edit tie: %+v", result.Commits[0].Links)
	}
	if !slices.Equal(result.Commits[0].CheckpointSessions, linktest.CheckpointSessions) {
		t.Fatal("unresolved checkpoint candidates were lost")
	}
}

// checkpointCandidates lets discovery tests check membership independently of
// the matcher. Unrelated checkpoint sessions must never appear in Links.
func checkpointCandidates(c Commit) map[string][]string {
	if len(c.CheckpointSessions) == 0 {
		return map[string][]string{}
	}
	ids := slices.Clone(c.CheckpointSessions)
	slices.Sort(ids)
	return map[string][]string{"entire_checkpoint": ids}
}

// A trailer with no valid checkpoint id, or one naming a checkpoint the clone
// does not hold, leaves the commit to the heuristic steps.
func TestResolveFallsBackFromAnUnusableTrailer(t *testing.T) {
	for _, trailer := range []string{
		"Entire-Checkpoint: not-a-checkpoint",
		"Entire-Checkpoint: not-a-checkpoint\nEntire-Checkpoint: 01ARZ3NDEKTSV4RRFFQ69G5FAL",
		// Only malformed values in a squash merge's body paragraphs.
		"* feat: one\n\nEntire-Checkpoint: not-a-checkpoint\n\n* feat: two\n\nCo-authored-by: Ada <ada@example.com>",
		// The key mid-line before prose names nothing.
		"Explains the Entire-Checkpoint: trailer.",
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
// record. Repeats collapse, a checkpoint the
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
	got := checkpointCandidates(result.Commits[0])
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
	got := checkpointCandidates(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["entire_checkpoint"], linktest.CheckpointSessions) {
		t.Fatalf("first commit links = %+v, want the valid checkpoint's sessions", result.Commits[0].Links)
	}
	if !strings.Contains(warnings.String(), `skipping malformed Entire-Checkpoint value "not-a-checkpoint"`) {
		t.Errorf("warnings = %q, want the skipped value named", warnings.String())
	}
}

// A GitHub squash merge copies each squashed commit's message, trailers
// included, into its body. The checkpoints those body paragraphs name link
// beside the one in the trailer block, as Entire reads the whole message, and
// a malformed body value is skipped with a warning.
func TestResolveLinksTheCheckpointsOfASquashMergeBody(t *testing.T) {
	const second, third = "0004a7a444a1", "0004a7a444a2"
	secondSessions := [2]string{"aaaaaaaa-1111-4000-8000-000000000001", "aaaaaaaa-1111-4000-8000-000000000002"}
	thirdSessions := [2]string{"aaaaaaaa-2222-4000-8000-000000000001", "aaaaaaaa-2222-4000-8000-000000000002"}
	f := linktest.NewWithTrailer(t, strings.Join([]string{
		"* feat: first step",
		"",
		linktest.CheckpointTrailer,
		"",
		"* feat: second step",
		"",
		"Entire-Checkpoint: not-a-checkpoint",
		"Entire-Checkpoint: " + second,
		"",
		"* chore: tidy",
		"",
		"---------",
		"",
		"Co-authored-by: Ada <ada@example.com>",
		"Entire-Checkpoint: " + third,
	}, "\n"))
	f.AddCheckpointEditing(t, "b.txt")
	f.AddCheckpointAt(t, second, secondSessions)
	f.AddCheckpointAt(t, third, thirdSessions)
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	result := resolve(t, req)
	if !result.Available {
		t.Fatalf("unavailable: %s: %s", result.Code, result.Reason)
	}
	want := append(append(slices.Clone(linktest.CheckpointSessions), secondSessions[:]...), thirdSessions[:]...)
	slices.Sort(want)
	got := checkpointCandidates(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["entire_checkpoint"], want) {
		t.Fatalf("first commit links = %+v, want all three checkpoints' sessions", result.Commits[0].Links)
	}
	if !strings.Contains(warnings.String(), `skipping malformed Entire-Checkpoint value "not-a-checkpoint"`) {
		t.Errorf("warnings = %q, want the skipped body value named", warnings.String())
	}
}

// Entire's pattern is not anchored to a line, so a checkpoint id after the key
// in the middle of a line contributes candidate membership.
func TestResolveLinksACheckpointNamedMidLine(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Carries over "+linktest.CheckpointTrailer+" from the first attempt.")
	f.AddCheckpointEditing(t, "b.txt")
	result := resolve(t, request(f))
	got := checkpointCandidates(result.Commits[0])
	if len(got) != 1 || !slices.Equal(got["entire_checkpoint"], linktest.CheckpointSessions) {
		t.Fatalf("first commit links = %+v, want the checkpoint's sessions", result.Commits[0].Links)
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
	fetchHint      = "git fetch origin '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'"
	branchHint     = "git fetch origin entire/checkpoints/v1"
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
			got := checkpointCandidates(result.Commits[0])
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
				if listed[id] {
					t.Errorf("unverified checkpoint session %s was listed: %+v", id, result.Sessions)
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
	if got := checkpointCandidates(result.Commits[0]); !slices.Equal(got["entire_checkpoint"], want) {
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
			if strings.Contains(warnings.String(), branchHint) {
				t.Errorf("warnings = %q; a ULID is never in the branch, so no branch hint", warnings.String())
			}
			if refs := linktest.Git(t, f.Repo, "for-each-ref", "refs/entire/checkpoints/"+ulidCheckpoint[len(ulidCheckpoint)-2:]+"/"+ulidCheckpoint); refs != "" {
				t.Errorf("the checkpoint ref appeared: %s", refs)
			}
		})
	}
}

// The fetch hint names the remote that is the document's repository, not
// origin: here origin is a fork and the repository is upstream.
func TestResolveFetchHintNamesTheMatchingRemote(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Entire-Checkpoint: "+ulidCheckpoint+"\nEntire-Checkpoint: ffffffffffff")
	linktest.Git(t, f.Repo, "remote", "rename", "origin", "upstream")
	linktest.Git(t, f.Repo, "remote", "add", "origin", "https://github.com/someone/widgets.git")
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	resolve(t, req)
	for _, want := range []string{
		"git fetch upstream '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'",
		"git fetch upstream entire/checkpoints/v1",
	} {
		if !strings.Contains(warnings.String(), want) {
			t.Errorf("warnings = %q, want it to contain %q", warnings.String(), want)
		}
	}
	if strings.Contains(warnings.String(), "git fetch origin") {
		t.Errorf("warnings = %q; origin is a fork, so no hint names it", warnings.String())
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
	var warnings bytes.Buffer
	req := request(f)
	req.Dir = partial
	req.Warn = &warnings
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
	if !strings.Contains(warnings.String(), "cannot be read") || strings.Contains(warnings.String(), "git fetch") {
		t.Errorf("warnings = %q, want a cannot-be-read warning with no fetch hint", warnings.String())
	}
}

// A 12-hex checkpoint may be in either store, so its warning names both
// fetches; and all the missing ids of a run share one line.
func TestResolveGroupsTheMissingCheckpointsInOneWarning(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Entire-Checkpoint: ffffffffffff\nEntire-Checkpoint: "+ulidCheckpoint)
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	resolve(t, req)
	lines := 0
	for _, line := range strings.Split(warnings.String(), "\n") {
		if strings.Contains(line, "not in this clone") {
			lines++
			for _, want := range []string{"ffffffffffff", ulidCheckpoint, fetchHint, branchHint} {
				if !strings.Contains(line, want) {
					t.Errorf("warning %q does not contain %q", line, want)
				}
			}
		}
	}
	if lines != 1 {
		t.Errorf("warnings = %q, want one line for both checkpoints", warnings.String())
	}

	// A hex id alone names the refs fetch and the branch fetch.
	hex := linktest.NewWithTrailer(t, "Entire-Checkpoint: ffffffffffff")
	warnings.Reset()
	req = request(hex)
	req.Warn = &warnings
	resolve(t, req)
	if !strings.Contains(warnings.String(), fetchHint) || !strings.Contains(warnings.String(), branchHint) {
		t.Errorf("warnings = %q, want both hints for a 12-hex id", warnings.String())
	}
}

// A checkpoint the clone holds whose metadata cannot be read is not "missing":
// the warning says it cannot be read, and suggests no fetch.
func TestResolveDoesNotSuggestAFetchForAnUnreadableCheckpoint(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
	f.AddCheckpoint(t)
	linktest.Git(t, f.Repo, "checkout", "-q", "entire/checkpoints/v1")
	path := filepath.Join(f.Repo, linktest.CheckpointID[:2], linktest.CheckpointID[2:], "0", "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("2026-07-26T15:52:20Z"), []byte("yesterday")), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "commit", "-qam", "break a date")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	result := resolve(t, req)
	if got := linkedTo(result.Commits[0]); len(got["entire_checkpoint"]) != 0 {
		t.Errorf("first commit links = %+v; the checkpoint is not readable", result.Commits[0].Links)
	}
	if !strings.Contains(warnings.String(), "cannot be read") || strings.Contains(warnings.String(), "git fetch") {
		t.Errorf("warnings = %q, want a cannot-be-read warning with no fetch hint", warnings.String())
	}
}

// A branch directory with no metadata.json is held but unreadable: it says so
// and suggests no fetch.
func TestResolveDoesNotSuggestAFetchForABranchDirectoryWithoutMetadata(t *testing.T) {
	f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
	f.AddCheckpoint(t)
	linktest.Git(t, f.Repo, "checkout", "-q", "entire/checkpoints/v1")
	linktest.Git(t, f.Repo, "rm", "-q", linktest.CheckpointID[:2]+"/"+linktest.CheckpointID[2:]+"/metadata.json")
	linktest.Git(t, f.Repo, "commit", "-qm", "drop the metadata")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	var warnings bytes.Buffer
	req := request(f)
	req.Warn = &warnings
	resolve(t, req)
	if !strings.Contains(warnings.String(), "cannot be read") || strings.Contains(warnings.String(), "git fetch") {
		t.Errorf("warnings = %q, want a cannot-be-read warning with no fetch hint", warnings.String())
	}
}

// When the clone cannot be asked whether it holds the checkpoints, the warning
// says they could not be checked, and suggests no fetch.
func TestWarnMissingSuggestsNoFetchWhenTheCheckFailed(t *testing.T) {
	var warnings bytes.Buffer
	warnMissing(&warnings, []string{ulidCheckpoint, "ffffffffffff"}, map[string][]string{"ffffffffffff": {"s"}}, nil,
		errors.New("boom"), map[string]string{ulidCheckpoint: "abc"}, "origin")
	got := warnings.String()
	if !strings.Contains(got, "could not check") || !strings.Contains(got, ulidCheckpoint) || !strings.Contains(got, "boom") {
		t.Errorf("warnings = %q, want the unchecked checkpoint and the error named", got)
	}
	if strings.Contains(got, "git fetch") || strings.Contains(got, "ffffffffffff") {
		t.Errorf("warnings = %q, want no fetch hint and no recorded checkpoint", got)
	}
}
