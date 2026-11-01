package reviewgroups

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/store"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(appdbtest.TempDir(t), "state", "groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGroupDefinitions(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	g, err := s.Create(ctx, "Acme/Demo", "  Release train  ", []int{9, 7})
	if err != nil {
		t.Fatal(err)
	}
	if g.Repository != "acme/demo" || g.Name != "Release train" || !slices.Equal(g.Members, []int{7, 9}) || len(g.ID) != 32 {
		t.Fatalf("created %+v", g)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil || got.Name != g.Name || !slices.Equal(got.Members, g.Members) {
		t.Fatalf("read back %+v %v", got, err)
	}
	name := "Renamed"
	if g, err = s.Update(ctx, g.ID, &name, []int{11, 7, 9}); err != nil || g.Name != "Renamed" || !slices.Equal(g.Members, []int{7, 9, 11}) {
		t.Fatalf("updated %+v %v", g, err)
	}
	if g, err = s.Update(ctx, g.ID, nil, nil); err != nil || g.Name != "Renamed" {
		t.Fatalf("an empty update changed %+v %v", g, err)
	}
	other, err := s.Create(ctx, "acme/demo", "Second", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := s.Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted group was read: %v", err)
	}
	if err := s.Delete(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	if _, err := s.Update(ctx, other.ID, &name, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted group was renamed: %v", err)
	}
	if list, _ = s.List(ctx); len(list) != 1 || list[0].ID != g.ID {
		t.Fatalf("list after delete %+v", list)
	}
}

func TestGroupRulesAreRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for name, test := range map[string]struct {
		name    string
		members []int
	}{
		"one member":   {"Group", []int{1}},
		"nine members": {"Group", []int{1, 2, 3, 4, 5, 6, 7, 8, 9}},
		"duplicate":    {"Group", []int{1, 1}},
		"not positive": {"Group", []int{0, 1}},
		"blank name":   {"   ", []int{1, 2}},
		"long name":    {strings.Repeat("x", 81), []int{1, 2}},
		"two lines":    {"One\nTwo", []int{1, 2}},
	} {
		_, err := s.Create(ctx, "acme/demo", test.name, test.members)
		var refused *InvalidError
		if !errors.As(err, &refused) || refused.Code != CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Create(ctx, "acme/demo", strings.Repeat("é", 80), []int{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Errorf("80 characters and 8 members are allowed: %v", err)
	}
	g, err := s.Create(ctx, "acme/demo", "Pair", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, g.ID, nil, []int{1}); err == nil {
		t.Error("a group was left with one member")
	}
}

func groupReview(t *testing.T, repository string, version int, members ...ReviewedMember) store.Version {
	t.Helper()
	document, err := json.Marshal(map[string]any{
		"review":  map[string]any{"repository": repository, "pull_request": 7, "version": version},
		"summary": map[string]any{"risk_score": map[string]any{"score": 3, "status": "complete"}},
		"stack":   map[string]any{"title": "Release train", "members": members},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store.Version{Metadata: store.Metadata{Version: version, Model: "stub-model"}, Document: document}
}

func TestGroupVersionsAreFencedAndKeyedByGroup(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	g, err := s.Create(ctx, "acme/demo", "Release train", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, "acme/demo", "Other", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Latest(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no version yet: %v", err)
	}
	reviewed := []ReviewedMember{{Number: 7, HeadSHA: "aaaaaaa", ReviewVersion: 2}, {Number: 8, HeadSHA: "bbbbbbb", ReviewVersion: 1}}
	publisher := Publisher{Store: s, Group: g.ID}
	if err := publisher.Put(ctx, store.Key{}, store.Version{}, groupReview(t, "acme/demo", 1, reviewed...)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, g.ID, 0, groupReview(t, "acme/demo", 1, reviewed...)); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second version 1: %v", err)
	}
	if err := s.Put(ctx, g.ID, 1, groupReview(t, "acme/other", 2, reviewed...)); err == nil {
		t.Fatal("another repository's review was stored")
	}
	if err := s.Put(ctx, g.ID, 1, groupReview(t, "acme/demo", 2)); err == nil {
		t.Fatal("a review without a stack was stored")
	}
	latest, err := publisher.Latest(ctx, store.Key{})
	if err != nil || latest.Metadata.Version != 1 {
		t.Fatalf("latest %+v %v", latest.Metadata, err)
	}
	if err := s.Put(ctx, g.ID, 1, groupReview(t, "acme/demo", 2, reviewed[:1]...)); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(ctx, g.ID)
	if err != nil || summary.VersionCount != 2 || summary.Latest.Version != 2 || len(summary.Reviewed) != 1 || !strings.Contains(string(summary.RiskScore), `"score":3`) {
		t.Fatalf("summary %+v %v", summary, err)
	}
	versions, err := s.Versions(ctx, g.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("versions %+v %v", versions, err)
	}
	if first, err := s.Version(ctx, g.ID, 1); err != nil || first.Metadata.Version != 1 {
		t.Fatalf("version 1 %+v %v", first.Metadata, err)
	}
	if summary, _ := s.Summary(ctx, other.ID); summary.VersionCount != 0 || summary.Latest != nil {
		t.Fatalf("another group sees the versions: %+v", summary)
	}
	// Deleting the group keeps its versions.
	if err := s.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if kept, err := s.Version(ctx, g.ID, 2); err != nil || kept.Metadata.Version != 2 {
		t.Fatalf("a deleted group's version is gone: %v", err)
	}
}

func TestOutdated(t *testing.T) {
	reviewed := []ReviewedMember{{Number: 7, HeadSHA: "aaa"}, {Number: 8, HeadSHA: "bbb"}}
	for name, test := range map[string]struct {
		members  []int
		reviewed []ReviewedMember
		heads    map[int]string
		want     bool
	}{
		"current":          {[]int{7, 8}, reviewed, map[int]string{7: "AAA", 8: "bbb"}, false},
		"unknown heads":    {[]int{8, 7}, reviewed, nil, false},
		"never reviewed":   {[]int{7, 8}, nil, map[int]string{7: "zzz"}, false},
		"a head moved":     {[]int{7, 8}, reviewed, map[int]string{8: "ccc"}, true},
		"a member added":   {[]int{7, 8, 9}, reviewed, nil, true},
		"a member removed": {[]int{7}, reviewed, nil, true},
	} {
		if got := Outdated(test.members, test.reviewed, test.heads); got != test.want {
			t.Errorf("%s: outdated %v, want %v", name, got, test.want)
		}
	}
}

func TestMembersFileKeepsCurrentSectionsAndActiveRisks(t *testing.T) {
	document := []byte(`{"review":{"head":"old"},"summary":{"title":"Notes","text":"Keeps notes.","change_kind":"feature","risk_score":{"score":4}},
"sections":[{"title":"Gone","kind":"logical","summary":{"text":"x"},"derived":{"state":"stale"}},
{"title":"Notes","kind":"logical","summary":{"text":"Adds notes."},"risks":[{"tier":"high","category":"security","text":"Open","state":"active"},{"tier":"low","category":"other","text":"Fixed","state":"mitigated"}]}]}`)
	raw, err := MembersFile([]MemberReview{{Number: 7, Head: "new", Version: 3, Document: document}})
	if err != nil {
		t.Fatal(err)
	}
	var file []memberContext
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file) != 1 || file[0].ReviewedHead != "old" || file[0].Head != "new" || file[0].ReviewVersion != 3 || *file[0].RiskScore != 4 {
		t.Fatalf("members.json %+v", file)
	}
	if sections := file[0].Sections; len(sections) != 1 || sections[0].Title != "Notes" || len(sections[0].Risks) != 1 || sections[0].Risks[0].Text != "Open" {
		t.Fatalf("sections %+v", file[0].Sections)
	}
	if _, err := MembersFile([]MemberReview{{Number: 8, Document: []byte("not json")}}); err == nil {
		t.Fatal("an unreadable review was accepted")
	}
}
