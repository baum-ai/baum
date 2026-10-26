package generate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/store"
)

// groupGitHub answers each pull request of the fixture clone with its own
// evidence.
type groupGitHub map[int]github.Evidence

func (g groupGitHub) Fetch(_ context.Context, _ github.Repository, number int, _ github.FetchOptions) (github.Evidence, error) {
	evidence, ok := g[number]
	if !ok {
		return github.Evidence{}, github.ErrNotFound
	}
	return evidence, nil
}

// groupOptions are a group run over the fixture clone's two pull requests:
// #7 (the feature branch) and #8 (the context-shift branch, which makes the
// same change to src/shift.go as #7).
func (f *fixture) groupOptions(t *testing.T, members ...generate.GroupMember) generate.Options {
	t.Helper()
	o := f.options(t)
	shift := f.repo.ShiftEvidence()
	shift.PullRequest.Number, shift.PullRequest.Title, shift.PullRequest.HeadRef = 8, "Raise the limit", "shift-feature"
	clone, err := gitlocal.Open(context.Background(), f.repo.Dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	o.Assembler = &assemble.Assembler{GitHub: groupGitHub{7: f.repo.Evidence(), 8: shift}, Git: clone, Sessions: &assembletest.Sessions{Found: assembletest.SessionTree()}}
	if members == nil {
		members = []generate.GroupMember{{Number: 7, Head: f.repo.Head, ReviewVersion: 3}, {Number: 8, ReviewVersion: 1}}
	}
	reviews, err := reviewgroups.MembersFile([]reviewgroups.MemberReview{{Number: 7, Head: f.repo.Head, Version: 3, Document: []byte(`{"review":{"head":"abc"},"summary":{"title":"Notes","text":"Keeps notes."},"sections":[{"title":"Notes","kind":"logical","summary":{"text":"Adds notes."},"risks":[{"tier":"medium","category":"data_integrity","text":"A token.","state":"active"}]}]}`)}})
	if err != nil {
		t.Fatal(err)
	}
	o.Group = &generate.Group{Name: "Release train", Members: members, MemberReviews: reviews}
	return o
}

type groupDocument struct {
	Review struct {
		PullRequest int    `json:"pull_request"`
		Head        string `json:"head"`
		Generator   struct {
			PromptVersion string `json:"prompt_version"`
		} `json:"generator"`
	} `json:"review"`
	Stack struct {
		Title   string                        `json:"title"`
		Members []reviewgroups.ReviewedMember `json:"members"`
	} `json:"stack"`
	Sections []struct {
		ID           string `json:"id"`
		PullRequests []int  `json:"pull_requests"`
	} `json:"sections"`
	Index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
		FileComparisons []struct {
			BaseRevision string `json:"base_revision"`
			Changes      []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"file_comparisons"`
	} `json:"index"`
}

// listSection adds a section covering only src/list.go, which only #7
// changes, and writes members the host must replace.
func listSection(d map[string]any) {
	sections := d["sections"].([]any)
	first := sections[0].(map[string]any)
	first["pull_requests"] = []any{99}
	second := map[string]any{}
	for key, value := range first {
		second[key] = value
	}
	second["id"], second["title"] = "sec_list", "The list"
	second["risks"], second["evidence"] = []any{}, []any{}
	second["attention"] = nil
	delete(second, "pull_requests")
	d["sections"] = append(sections, second)
	d["stack"] = map[string]any{"title": "written by the model", "members": []any{}}
}

func TestGroupRunReviewsEveryMemberAsOneDocument(t *testing.T) {
	f := newFixture(t)
	var listHunks []string
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		for _, path := range []string{"members/7/head/src/list.go", "members/8/head/src/shift.go", "members/8/merge-base/src/shift.go", "members.json"} {
			if _, err := os.Stat(filepath.Join(request.Directory, path)); err != nil {
				t.Errorf("the run directory lacks %s: %v", path, err)
			}
		}
		if _, err := os.Stat(filepath.Join(request.Directory, "head")); err == nil {
			t.Error("a group run has a top-level head/ tree")
		}
		if !strings.Contains(request.Prompt, "## Reviewing a group of pull requests") {
			t.Error("the prompt is not the group prompt")
		}
		raw, err := os.ReadFile(filepath.Join(request.Directory, "bundle.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var b struct {
			Diff []struct{ ID, Path string } `json:"diff"`
		}
		_ = json.Unmarshal(raw, &b)
		document, err := generatetest.Document(request.Directory, func(d map[string]any) {
			listSection(d)
			sections := d["sections"].([]any)
			second := sections[1].(map[string]any)
			var hunks []any
			for _, hunk := range b.Diff {
				if hunk.Path == "src/list.go" {
					hunks = append(hunks, hunk.ID)
					listHunks = append(listHunks, hunk.ID)
				}
			}
			second["hunks"] = hunks
			second["summary"] = map[string]any{"text": "The list.", "anchors": []any{map[string]any{"type": "hunk", "id": hunks[0]}}}
			second["assessment"].(map[string]any)["reasons"] = []any{map[string]any{"text": "Lists change.", "anchors": []any{map[string]any{"type": "hunk", "id": hunks[0]}}}}
		})
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 100}, err
	}
	groups, err := reviewgroups.Open(filepath.Join(t.TempDir(), "groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = groups.Close() }()
	group, err := groups.Create(context.Background(), "acme/demo", "Release train", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	publisher := reviewgroups.Publisher{Store: groups, Group: group.ID}
	key := store.Key{Repository: "acme/demo", Number: 7}
	result, err := generate.Publish(context.Background(), publisher, key, f.groupOptions(t), nil)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, f.progress.String())
	}
	var doc groupDocument
	if err := json.Unmarshal(result.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Review.PullRequest != 7 || doc.Review.Head != f.repo.Head || doc.Review.Generator.PromptVersion != "group-review-public-v1" {
		t.Errorf("review block %+v", doc.Review)
	}
	want := []reviewgroups.ReviewedMember{{Number: 7, HeadSHA: f.repo.Head, ReviewVersion: 3}, {Number: 8, HeadSHA: f.repo.ShiftHead, ReviewVersion: 1}}
	if doc.Stack.Title != "Release train" || !slices.Equal(doc.Stack.Members, want) {
		t.Errorf("stack %+v, want members %+v", doc.Stack, want)
	}
	if len(doc.Sections) != 2 || !slices.Equal(doc.Sections[0].PullRequests, []int{7, 8}) || !slices.Equal(doc.Sections[1].PullRequests, []int{7}) {
		t.Errorf("section members %+v", doc.Sections)
	}
	// The change both members make to src/shift.go is one hunk.
	shift := 0
	for _, hunk := range doc.Index.Hunks {
		if hunk.Path == "src/shift.go" {
			shift++
		}
	}
	if shift != 1 || len(listHunks) == 0 {
		t.Errorf("src/shift.go hunks: %d, src/list.go hunks: %v", shift, listHunks)
	}
	if len(doc.Index.FileComparisons) != 2 || doc.Index.FileComparisons[0].BaseRevision != f.repo.MergeBase || doc.Index.FileComparisons[1].BaseRevision != f.repo.ShiftBase {
		t.Errorf("file comparisons %+v", doc.Index.FileComparisons)
	}
	if paths := doc.Index.FileComparisons[1].Changes; len(paths) != 1 || paths[0].Path != "src/shift.go" {
		t.Errorf("#8 compares %+v", paths)
	}
	summary, err := groups.Summary(context.Background(), group.ID)
	if err != nil || summary.VersionCount != 1 || !slices.Equal(summary.Reviewed, want) {
		t.Fatalf("stored summary %+v %v", summary, err)
	}
	if reviewgroups.Outdated(group.Members, summary.Reviewed, map[int]string{7: f.repo.Head, 8: f.repo.ShiftHead}) {
		t.Error("a current group review reads as outdated")
	}
	f.requireClean(t)

	// #8 leaves the group: the next version carries the sections with #7
	// only.
	f.backend.RunFunc = nil
	if _, err := generate.Publish(context.Background(), publisher, key, f.groupOptions(t, generate.GroupMember{Number: 7, ReviewVersion: 3}), nil); err != nil {
		t.Fatalf("second run: %v\n%s", err, f.progress.String())
	}
	latest, err := groups.Latest(context.Background(), group.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc = groupDocument{}
	if err := json.Unmarshal(latest.Document, &doc); err != nil {
		t.Fatal(err)
	}
	for _, section := range doc.Sections {
		if slices.Contains(section.PullRequests, 8) {
			t.Errorf("section %s still names #8: %v", section.ID, section.PullRequests)
		}
	}
	if latest.Metadata.Version != 2 || len(doc.Stack.Members) != 1 {
		t.Errorf("second version %d with stack %+v", latest.Metadata.Version, doc.Stack)
	}
}

func TestGroupRunStopsWhenAMemberHeadMoved(t *testing.T) {
	f := newFixture(t)
	_, err := generate.Run(context.Background(), f.groupOptions(t, generate.GroupMember{Number: 7, Head: f.repo.Head, ReviewVersion: 1}, generate.GroupMember{Number: 8, Head: "0123456789abcdef", ReviewVersion: 1}))
	if generate.CategoryOf(err) != generate.CategoryInput || !strings.Contains(err.Error(), "#8 head changed") {
		t.Fatalf("moved head: %v", err)
	}
	if len(f.backend.Requests) != 0 {
		t.Fatal("a model was called for a moved head")
	}
	if _, err := generate.Run(context.Background(), f.groupOptions(t, generate.GroupMember{Number: 7, ReviewVersion: 0})); generate.CategoryOf(err) != generate.CategoryInput {
		t.Fatalf("a member without a saved review: %v", err)
	}
	f.requireClean(t)
}
