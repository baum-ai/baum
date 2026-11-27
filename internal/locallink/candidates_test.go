package locallink

import (
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/linkage"
)

// Only a recorded creation links a commit. Editing its files, tying with
// another editor or starting from its parent never does: those linked
// colleagues' commits to sessions here that edited the same files.
func TestCommitEdgesNeedARecordedCreation(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Minute)
	commit := linkage.EnrichedCommit{
		Commit:      linkage.Commit{OID: "abcdef0123456789", CommittedAt: start.Add(30 * time.Minute).Format(time.RFC3339)},
		AncestorOID: "parent", FilesTouched: []string{"feature.go"},
	}
	editor := linkage.RunCandidate{RunID: "editor", StartedAt: &start, EndedAt: &end,
		FilesEdited: []string{"feature.go"}, FilesTouched: []string{"feature.go"}}
	other := editor
	other.RunID = "other"
	reader := linkage.RunCandidate{RunID: "reader", StartedAt: &start, EndedAt: &end,
		StartingCommitSHA: "parent", FilesTouched: []string{"feature.go"}}
	for _, tc := range []struct {
		name       string
		candidates []linkage.RunCandidate
	}{
		{"an editor", []linkage.RunCandidate{editor}},
		{"tied editors", []linkage.RunCandidate{editor, other}},
		{"a reader sharing the parent", []linkage.RunCandidate{reader}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edges, err := commitEdges(commit, tc.candidates, linkage.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if len(edges) != 0 {
				t.Fatalf("linked without a recorded creation: %+v", edges)
			}
		})
	}
}

// Every session that recorded creating the commit links, by full id or an
// unambiguous prefix; an editor beside them does not.
func TestCommitEdgesLinkEveryCreator(t *testing.T) {
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "abcdef0123456789", CommittedAt: "2026-07-01T11:00:00Z"}, FilesTouched: []string{"feature.go"}}
	editor := linkage.RunCandidate{RunID: "editor", FilesEdited: []string{"feature.go"}}
	creator := linkage.RunCandidate{RunID: "creator", CommitRefs: []linkage.CommitRef{{SHA: commit.OID, Role: linkage.RoleCreated}}}
	amender := linkage.RunCandidate{RunID: "amender", CommitRefs: []linkage.CommitRef{{SHA: commit.OID[:7], Role: linkage.RoleCreated}}}
	mentioner := linkage.RunCandidate{RunID: "mentioner", CommitRefs: []linkage.CommitRef{{SHA: commit.OID, Role: linkage.RoleMentioned}}}
	edges, err := commitEdges(commit, []linkage.RunCandidate{editor, creator, amender, mentioner}, linkage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, edge := range edges {
		if edge.Method != linkage.MethodCommitRef || edge.Confidence != linkage.DefaultConfig().CommitRefConfidence {
			t.Fatalf("edge = %+v, want a commit_ref", edge)
		}
		got = append(got, edge.RunID)
	}
	if !slices.Equal(got, []string{"creator", "amender"}) {
		t.Fatalf("linked %v, want the two creators", got)
	}
}

func TestCommitEdgesSkipMergeCommits(t *testing.T) {
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "abcdef0123456789"}, IsMerge: true}
	creator := linkage.RunCandidate{RunID: "creator", CommitRefs: []linkage.CommitRef{{SHA: commit.OID, Role: linkage.RoleCreated}}}
	edges, err := commitEdges(commit, []linkage.RunCandidate{creator}, linkage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("merge commit linked: %+v", edges)
	}
}
