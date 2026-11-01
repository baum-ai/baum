package locallink

import (
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/linkage"
)

func TestCheckpointCandidatesRequireIndependentEvidence(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Minute)
	commit := linkage.EnrichedCommit{
		Commit:      linkage.Commit{OID: "abcdef0123456789", CommittedAt: start.Add(30 * time.Minute).Format(time.RFC3339)},
		AncestorOID: "parent", FilesTouched: []string{"feature.go"},
	}
	editor := linkage.RunCandidate{RunID: "editor", StartedAt: &start, EndedAt: &end,
		FilesEdited: []string{"feature.go"}, FilesTouched: []string{"feature.go"}}
	reader := linkage.RunCandidate{RunID: "reader", StartedAt: &start, EndedAt: &end,
		StartingCommitSHA: "parent", FilesTouched: []string{"feature.go"}}
	for _, tc := range []struct {
		name       string
		candidates []linkage.RunCandidate
		want       []string
	}{
		{"one editor and a reader", []linkage.RunCandidate{editor, reader}, []string{"editor"}},
		{"reader sharing parent", []linkage.RunCandidate{reader}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edges, err := commitEdges(commit, tc.candidates, linkage.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, edge := range edges {
				got = append(got, edge.RunID)
				if edge.Method != linkage.MethodFilesOverlap || edge.Confidence == 1 {
					t.Fatalf("unsupported edge: %+v", edge)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sessions = %v, want %v", got, tc.want)
			}
		})
	}
	for _, offset := range []time.Duration{-48 * time.Hour, time.Hour} {
		at := start.Add(offset)
		candidate := editor
		candidate.StartedAt, candidate.EndedAt = &at, &at
		edges, err := commitEdges(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		if len(edges) != 0 {
			t.Fatalf("out-of-window checkpoint candidate linked: %+v", edges)
		}
	}
}

func TestLocalFileTiesStayUnresolved(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "abcdef0123456789", CommittedAt: end.Format(time.RFC3339)}, FilesTouched: []string{"feature.go"}}
	first := linkage.RunCandidate{RunID: "first", StartedAt: &start, EndedAt: &end, FilesEdited: []string{"feature.go"}}
	second := first
	second.RunID = "second"
	edges, err := commitEdges(commit, []linkage.RunCandidate{first, second}, linkage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("file tie became attribution: %+v", edges)
	}
}

func TestCommitCreatorIsKeptBesideEditor(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{OID: "abcdef0123456789", CommittedAt: end.Format(time.RFC3339)}, FilesTouched: []string{"feature.go"}}
	editor := linkage.RunCandidate{RunID: "editor", StartedAt: &start, EndedAt: &end, FilesEdited: []string{"feature.go"}}
	creator := linkage.RunCandidate{RunID: "creator", CommitRefs: []linkage.CommitRef{{SHA: commit.OID, Role: linkage.RoleCreated}}}
	edges, err := commitEdges(commit, []linkage.RunCandidate{editor, creator}, linkage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 || edges[0].RunID != "creator" || edges[0].Method != linkage.MethodCommitRef || edges[1].RunID != "editor" {
		t.Fatalf("links = %+v; want creator and corroborated editor", edges)
	}
}
