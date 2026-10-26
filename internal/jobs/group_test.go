package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/store"
)

// A group job is keyed by its group, never by a pull request: it runs beside
// the members' own jobs, and a second job of the same group waits on it as a
// pull request's does.
func TestGroupJobsAreKeyedByTheirGroup(t *testing.T) {
	q, f := newQueue(t)
	ctx := context.Background()
	group := estimate(t, q, "single", Item{Clone: "clone", Group: "g1"})
	jobs, err := q.Submit(ctx, group.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	started := <-f.started
	if started.Group != "g1" || started.PR != 0 || jobs[0].Group != "g1" {
		t.Fatalf("group job %+v started as %+v", jobs[0], started)
	}
	member := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	if _, err := q.Submit(ctx, member.ID, true, false); err != nil {
		t.Fatalf("a member's own job waited on the group's: %v", err)
	}
	again := estimate(t, q, "single", Item{Clone: "clone", Group: "g1"})
	if _, err := q.Submit(ctx, again.ID, true, false); err == nil {
		t.Fatal("a second job of a running group was accepted")
	}
	other := estimate(t, q, "single", Item{Clone: "clone", Group: "g2"})
	if _, err := q.Submit(ctx, other.ID, true, false); err != nil {
		t.Fatalf("another group's job was refused: %v", err)
	}
	f.release <- struct{}{}
	waitState(t, q, jobs[0].ID, "completed")
}

func TestGroupJobRefusalsOverHTTP(t *testing.T) {
	q, f := newQueue(t)
	f.prepareErr = &MembersNeedReviewsError{Numbers: []int{2, 3}}
	server := httptest.NewServer(q.Handler())
	defer server.Close()
	for _, path := range []string{"/api/jobs/estimate", "/api/jobs"} {
		response, err := http.Post(server.URL+path, "application/json", strings.NewReader(`{"items":[{"clone":"clone","pr":0,"group":"g1"}],"mode":"single"}`))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Missing []int `json:"members_need_reviews"`
		}
		_ = json.NewDecoder(response.Body).Decode(&body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusConflict || !slices.Equal(body.Missing, []int{2, 3}) {
			t.Fatalf("%s: %d %+v", path, response.StatusCode, body)
		}
	}
	f.prepareErr = reviewgroups.ErrNotFound
	response, err := http.Post(server.URL+"/api/jobs", "application/json", strings.NewReader(`{"items":[{"clone":"clone","pr":0,"group":"gone"}],"mode":"single"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a deleted group answered %d", response.StatusCode)
	}
}

// saveReview stores a minimal saved review of pull request number in the
// clone's version store.
func saveReview(t *testing.T, commonDir string, number int) {
	t.Helper()
	versions, err := store.Open(commonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	document, _ := json.Marshal(map[string]any{"schema": "baum.review/1", "review": map[string]any{"repository": "owner/repo", "pull_request": number, "version": 1, "head": "first"}, "summary": map[string]any{"title": "A member", "text": "What it changes."}, "sections": []any{}})
	if err := versions.Put(context.Background(), store.Key{Repository: "owner/repo", Number: number}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: "first"}, Document: document}); err != nil {
		t.Fatal(err)
	}
}

// Preparing a group reads every member from GitHub and needs each member's
// saved review; the confirmation covers every member's head and, for the
// whole group, a member that needs isolation.
func TestPrepareGroupReadsEveryMemberAndNeedsTheirReviews(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	groups, err := reviewgroups.Open(filepath.Join(t.TempDir(), "groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = groups.Close() })
	group, err := groups.Create(ctx, "owner/repo", "Release train", []int{2, 1})
	if err != nil {
		t.Fatal(err)
	}
	local := localRunner(registry, "owner/repo", "write", 200)
	local.Groups = groups
	_, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID})
	var missing *MembersNeedReviewsError
	if !errors.As(err, &missing) || !slices.Equal(missing.Numbers, []int{1, 2}) {
		t.Fatalf("members without reviews: %v", err)
	}
	saveReview(t, clone.CommonDir, 1)
	if _, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID}); !errors.As(err, &missing) || !slices.Equal(missing.Numbers, []int{2}) {
		t.Fatalf("one member without a review: %v", err)
	}
	saveReview(t, clone.CommonDir, 2)
	p, _, err := local.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p.PR != 0 || p.Group != group.ID || p.GroupName != "Release train" || p.Members != "1@first,2@first" || p.HeadSHA != "first" || p.RequiresIsolationConfirmation {
		t.Fatalf("prepared %+v", p)
	}
	fork := localRunner(registry, "other/repo", "write", 200)
	fork.Groups = groups
	if p, _, err = fork.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID}); err != nil || !p.RequiresIsolationConfirmation {
		t.Fatalf("a fork member must need isolation for the group: %+v %v", p, err)
	}
	if _, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID, PR: 1}); err == nil {
		t.Fatal("an item naming a group and a pull request was accepted")
	}
	elsewhere, err := groups.Create(ctx, "owner/other", "Elsewhere", []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: elsewhere.ID}); err == nil {
		t.Fatal("a clone of another repository prepared the group")
	}
	if err := groups.Delete(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: group.ID}); !errors.Is(err, reviewgroups.ErrNotFound) {
		t.Fatalf("a deleted group: %v", err)
	}
	local.Groups = nil
	if _, _, err = local.Prepare(ctx, Item{Clone: clone.ID, Group: elsewhere.ID}); !errors.Is(err, ErrGroupsUnavailable) {
		t.Fatalf("no group store: %v", err)
	}
}
