package commitlabel

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var a = strings.Repeat("a", 40)
var h = strings.Repeat("b", 40)
var old = strings.Repeat("c", 40)
var treeA = strings.Repeat("1", 40)
var treeH = strings.Repeat("2", 40)

func at(n int) *time.Time              { t := time.Date(2026, 10, 20, 0, 0, n, 0, time.UTC); return &t }
func interval(start, end int) Interval { return Interval{at(start), at(end)} }
func ref(sha string, n int) CommitRef {
	return CommitRef{SHA: sha, Role: "created", RepositoryID: "repo", ObservedAt: at(n)}
}
func edit(start, end int) Edit {
	return Edit{ToolCallID: "edit", RepositoryID: "repo", Interval: interval(start, end)}
}

type repository map[string]Commit

type resolveFunc func(context.Context, string) (Commit, error)

func (f resolveFunc) ValidationCommit(ctx context.Context, id string) (Commit, error) {
	return f(ctx, id)
}

func (r repository) ValidationCommit(_ context.Context, id string) (Commit, error) {
	var matches []Commit
	for sha, c := range r {
		if strings.HasPrefix(sha, id) {
			matches = append(matches, c)
		}
	}
	if len(matches) != 1 {
		return Commit{}, errors.New("missing or ambiguous")
	}
	return matches[0], nil
}
func fixture() (Input, repository) {
	return Input{RepositoryID: "repo", HeadSHA: h, PRCommits: []string{a, h}, Calls: []Call{{ID: "check", RepositoryID: "repo", Interval: interval(10, 11)}}, CommitRefs: []CommitRef{ref(h, 20)}}, repository{a: {SHA: a, TreeSHA: treeA}, h: {SHA: h, TreeSHA: treeH}, old: {SHA: old, TreeSHA: treeH}}
}

func TestLabels(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Input, repository)
		cites  []string
		want   Kind
		sha    string
		behind int
	}{
		{name: "no citations", cites: []string{}, want: Unknown},
		{name: "edit strictly during cited call", change: func(in *Input, _ repository) {
			in.Calls[0].Interval = interval(10, 15)
			in.Edits = []Edit{edit(12, 13)}
		}, want: Unknown},
		{name: "check before commit without edits", want: Head, sha: h},
		{name: "check after final commit", change: func(in *Input, _ repository) { in.Calls[0].Interval = interval(30, 31) }, want: Head, sha: h},
		{name: "edit after check excludes later head", change: func(in *Input, _ repository) {
			in.CommitRefs = append(in.CommitRefs, ref(a, 5))
			in.Edits = []Edit{edit(12, 13)}
		}, want: Earlier, sha: a, behind: 1},
		{name: "uncommitted edits", change: func(in *Input, _ repository) {
			in.CommitRefs = append(in.CommitRefs, ref(a, 5))
			in.Edits = []Edit{edit(8, 9), edit(12, 13)}
		}, want: Uncommitted},
		{name: "amend same tree", change: func(in *Input, _ repository) { in.CommitRefs = []CommitRef{ref(old, 5)} }, want: Head, sha: old},
		{name: "rebase same tree resolves old abbreviation", change: func(in *Input, _ repository) { in.CommitRefs = []CommitRef{ref(old[:7], 5)} }, want: Head, sha: old},
		{name: "no commit evidence", change: func(in *Input, _ repository) { in.CommitRefs = nil }, want: Unknown},
		{name: "starting is not creation", change: func(in *Input, _ repository) { in.CommitRefs[0].Role = "starting" }, want: Unknown},
		{name: "mentioned is not creation", change: func(in *Input, _ repository) { in.CommitRefs[0].Role = "mentioned" }, want: Unknown},
		{name: "matching outside PR different tree", change: func(in *Input, r repository) { in.CommitRefs = []CommitRef{ref(old, 5)}; r[old] = Commit{old, treeA} }, want: Unknown},
		{name: "missing head", change: func(_ *Input, r repository) { delete(r, h) }, want: Unknown},
		{name: "missing matching commit", change: func(in *Input, r repository) { in.CommitRefs = []CommitRef{ref(old, 5)}; delete(r, old) }, want: Unknown},
		{name: "missing PR object", change: func(_ *Input, r repository) { delete(r, a) }, want: Unknown},
		{name: "missing tree", change: func(_ *Input, r repository) { r[h] = Commit{SHA: h} }, want: Unknown},
		{name: "unverified prefix", change: func(in *Input, r repository) { in.CommitRefs = []CommitRef{ref(old[:7], 5)}; r[old] = Commit{h, treeH} }, want: Unknown},
		{name: "missing commit timestamp", change: func(in *Input, _ repository) { in.CommitRefs[0].ObservedAt = nil }, want: Unknown},
		{name: "missing cited call timestamp", change: func(in *Input, _ repository) { in.Calls[0].StartedAt = nil }, want: Unknown},
		{name: "missing call result timestamp", change: func(in *Input, _ repository) { in.Calls[0].EndedAt = nil }, want: Unknown},
		{name: "reversed call", change: func(in *Input, _ repository) { in.Calls[0].Interval = interval(11, 10) }, want: Unknown},
		{name: "unknown edit timestamp", change: func(in *Input, _ repository) { e := edit(1, 2); e.EndedAt = nil; in.Edits = []Edit{e} }, want: Unknown},
		{name: "unowned edit", change: func(in *Input, _ repository) { e := edit(1, 2); e.ToolCallID = ""; in.Edits = []Edit{e} }, want: Unknown},
		{name: "other repository edit ignored", change: func(in *Input, _ repository) { e := edit(12, 13); e.RepositoryID = "other"; in.Edits = []Edit{e} }, want: Head, sha: h},
		{name: "unscoped edit unknown", change: func(in *Input, _ repository) { e := edit(12, 13); e.RepositoryID = ""; in.Edits = []Edit{e} }, want: Unknown},
		{name: "other repository commit ignored", change: func(in *Input, _ repository) { in.CommitRefs[0].RepositoryID = "other" }, want: Unknown},
		{name: "cited call edits", change: func(in *Input, _ repository) { e := edit(10, 11); e.ToolCallID = "check"; in.Edits = []Edit{e} }, want: Unknown},
		{name: "overlapping edit", change: func(in *Input, _ repository) { in.Edits = []Edit{edit(9, 12)} }, want: Unknown},
		{name: "edit at call boundary", change: func(in *Input, _ repository) { in.Edits = []Edit{edit(10, 10)} }, want: Unknown},
		{name: "edit at commit boundary", change: func(in *Input, _ repository) { in.Edits = []Edit{edit(20, 20)} }, want: Unknown},
		{name: "edit beyond interval", change: func(in *Input, _ repository) { in.Edits = []Edit{edit(21, 22)} }, want: Head, sha: h},
		{name: "latest cited call", change: func(in *Input, _ repository) {
			in.Calls = append(in.Calls, Call{ID: "first", RepositoryID: "repo", Interval: interval(2, 3)})
			in.Edits = []Edit{edit(5, 6)}
		}, cites: []string{"check", "first"}, want: Head, sha: h},
		{name: "latest tied", change: func(in *Input, _ repository) { c := in.Calls[0]; c.ID = "tied"; in.Calls = append(in.Calls, c) }, cites: []string{"check", "tied"}, want: Unknown},
		{name: "earlier tie does not hide latest", change: func(in *Input, _ repository) {
			in.Calls = append(in.Calls, Call{ID: "first", RepositoryID: "repo", Interval: interval(2, 3)}, Call{ID: "second", RepositoryID: "repo", Interval: interval(2, 3)})
		}, cites: []string{"first", "second", "check"}, want: Head, sha: h},
		{name: "missing citation", cites: []string{"check", "absent"}, want: Unknown},
		{name: "duplicate citations", cites: []string{"check", "check"}, want: Head, sha: h},
		{name: "duplicate call identity", change: func(in *Input, _ repository) { in.Calls = append(in.Calls, in.Calls[0]) }, want: Unknown},
		{name: "PR does not end at head", change: func(in *Input, _ repository) { in.PRCommits = []string{h, a} }, want: Unknown},
		{name: "duplicate PR commit", change: func(in *Input, _ repository) { in.PRCommits = []string{a, a, h} }, want: Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, r := fixture()
			if tt.change != nil {
				tt.change(&in, r)
			}
			cites := tt.cites
			if cites == nil {
				cites = []string{"check"}
			}
			got := Compute(context.Background(), r, in, cites)
			if got.Kind != tt.want || got.CommitSHA != tt.sha || got.CommitsBehind != tt.behind {
				t.Fatalf("got %+v, want %s %s behind=%d", got, tt.want, tt.sha, tt.behind)
			}
			if got.Method == "" || got.Reason == "" {
				t.Fatalf("missing provenance: %+v", got)
			}
			if strings.Contains(strings.ToLower(got.Reason), "identical working tree") {
				t.Fatal("overclaims working-tree equality")
			}
		})
	}
}

func TestMissingMirrorNeverLabelsHead(t *testing.T) {
	in, _ := fixture()
	if got := Compute(context.Background(), nil, in, []string{"check"}); got.Kind != Unknown || got.Reason == "" {
		t.Fatalf("%+v", got)
	}
}

func TestConflictingCanonicalObjectEvidenceIsUnknown(t *testing.T) {
	in, r := fixture()
	source := resolveFunc(func(ctx context.Context, id string) (Commit, error) {
		if id == a[:7] {
			return Commit{SHA: a, TreeSHA: treeH}, nil
		}
		return r.ValidationCommit(ctx, id)
	})
	for _, refs := range [][]CommitRef{{ref(a[:7], 5)}, {ref(h, 20), ref(a[:7], 5)}} {
		in.CommitRefs = refs
		if got := Compute(context.Background(), source, in, []string{"check"}); got.Kind != Unknown {
			t.Fatalf("conflicting tree evidence produced %+v", got)
		}
	}
}
func TestOrderDoesNotChangeLabel(t *testing.T) {
	in, r := fixture()
	in.CommitRefs = append(in.CommitRefs, ref(old, 5), ref(a, 4))
	want := Compute(context.Background(), r, in, []string{"check"})
	slices.Reverse(in.CommitRefs)
	got := Compute(context.Background(), r, in, []string{"check"})
	if !reflect.DeepEqual(got, want) || got.CommitSHA != h {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}
