package reviewgroups

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

type fakeRegistry []repos.Repository

func (r fakeRegistry) Repositories(context.Context) ([]repos.Repository, error) { return r, nil }

type apiFixture struct {
	store   *Store
	server  *httptest.Server
	changed int
	heads   map[int]string
	patches []locallink.PatchRequest
}

func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	f := &apiFixture{store: openStore(t), heads: map[int]string{}}
	api := &API{
		Store:    f.store,
		Registry: fakeRegistry{{Repository: "acme/demo", Clones: []repos.Clone{{ID: "c1", Path: "/clone", Repository: "acme/demo", Available: true}}}, {Repository: "acme/other"}},
		Index: func(context.Context) (reviewindex.Snapshot, error) {
			var entries []reviewindex.Entry
			for number, head := range f.heads {
				entries = append(entries, reviewindex.Entry{PullRequest: discovery.PullRequest{Repository: "acme/demo", Number: number, HeadSHA: head}})
			}
			return reviewindex.Snapshot{Entries: entries}, nil
		},
		Changed: func() { f.changed++ },
		Patch: func(_ context.Context, request locallink.PatchRequest) (locallink.PatchResult, error) {
			f.patches = append(f.patches, request)
			if request.Head == "bbbbbbb" {
				return locallink.PatchResult{Available: true, Patch: "@@ -10,2 +12,3 @@\n a\n+b\n c\n"}, nil
			}
			return locallink.PatchResult{Available: true, Patch: "@@ -1,2 +1,3 @@\n a\n+b\n c\n"}, nil
		},
	}
	f.server = httptest.NewServer(api.Handler())
	t.Cleanup(f.server.Close)
	return f
}

func (f *apiFixture) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, raw
}

func code(raw []byte) string {
	var body struct{ Code string }
	_ = json.Unmarshal(raw, &body)
	return body.Code
}

func TestCreateRefusesCrossRepositoryAndUnpickedGroups(t *testing.T) {
	f := newAPI(t)
	status, raw := f.do(t, "POST", "/api/groups", `{"name":"Mixed","members":[{"repository":"acme/demo","number":7},{"repository":"acme/other","number":8}]}`)
	if status != http.StatusBadRequest || code(raw) != CodeCrossRepository || !strings.Contains(string(raw), "not supported yet") {
		t.Fatalf("cross repository: %d %s", status, raw)
	}
	status, raw = f.do(t, "POST", "/api/groups", `{"name":"Unpicked","members":[{"repository":"acme/nope","number":7},{"repository":"acme/nope","number":8}]}`)
	if status != http.StatusBadRequest || code(raw) != CodeNotPicked {
		t.Fatalf("unpicked: %d %s", status, raw)
	}
	status, raw = f.do(t, "POST", "/api/groups", `{"name":"Alone","members":[{"repository":"acme/demo","number":7}]}`)
	if status != http.StatusBadRequest || code(raw) != CodeInvalid {
		t.Fatalf("one member: %d %s", status, raw)
	}
	if f.changed != 0 {
		t.Fatal("a refused request published a change")
	}
}

func TestGroupLifecycleOverHTTP(t *testing.T) {
	f := newAPI(t)
	status, raw := f.do(t, "POST", "/api/groups", `{"name":"Release train","members":[{"repository":"ACME/demo","number":8},{"repository":"acme/demo","number":7}]}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var created View
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Repository != "acme/demo" || len(created.Members) != 2 || created.VersionCount != 0 || created.Outdated || f.changed != 1 {
		t.Fatalf("created %+v", created)
	}
	id := created.ID
	if status, raw = f.do(t, "PATCH", "/api/groups/"+id, `{"name":"Renamed","members":[7,8,9]}`); status != http.StatusOK || !strings.Contains(string(raw), `"members":[7,8,9]`) {
		t.Fatalf("update: %d %s", status, raw)
	}
	if status, _ = f.do(t, "PATCH", "/api/groups/"+id, `{"color":"red"}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", status)
	}
	if status, _ = f.do(t, "GET", "/api/groups/"+id+"/document", ""); status != http.StatusNotFound {
		t.Fatalf("a group without a review served a document: %d", status)
	}
	// A review of members 7 and 8 (before 9 joined) is outdated.
	reviewed := []ReviewedMember{{Number: 7, HeadSHA: "aaaaaaa", ReviewVersion: 1}, {Number: 8, HeadSHA: "bbbbbbb", ReviewVersion: 1}}
	version := groupReview(t, "acme/demo", 1, reviewed...)
	if err := f.store.Put(context.Background(), id, 0, version); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Available bool   `json:"available"`
		Groups    []View `json:"groups"`
	}
	_, raw = f.do(t, "GET", "/api/groups", "")
	if err := json.Unmarshal(raw, &list); err != nil || !list.Available || len(list.Groups) != 1 || !list.Groups[0].Outdated || list.Groups[0].VersionCount != 1 {
		t.Fatalf("list %s", raw)
	}
	if status, _ = f.do(t, "PATCH", "/api/groups/"+id, `{"members":[7,8]}`); status != http.StatusOK {
		t.Fatal("remove a member")
	}
	f.heads = map[int]string{7: "aaaaaaa", 8: "bbbbbbb"}
	var one View
	_, raw = f.do(t, "GET", "/api/groups/"+id, "")
	if err := json.Unmarshal(raw, &one); err != nil || one.Outdated || one.Latest == nil || one.Latest.Version != 1 {
		t.Fatalf("current group %s", raw)
	}
	f.heads[8] = "ccccccc"
	if _, raw = f.do(t, "GET", "/api/groups/"+id, ""); !strings.Contains(string(raw), `"outdated":true`) {
		t.Fatalf("a moved head is not outdated: %s", raw)
	}
	if status, raw = f.do(t, "GET", "/api/groups/"+id+"/document?version=1", ""); status != http.StatusOK || !strings.Contains(string(raw), `"stack"`) {
		t.Fatalf("document: %d %s", status, raw)
	}
	if status, raw = f.do(t, "GET", "/api/groups/"+id+"/versions", ""); status != http.StatusOK || !strings.Contains(string(raw), `"version":1`) {
		t.Fatalf("versions: %d %s", status, raw)
	}
	if status, _ = f.do(t, "DELETE", "/api/groups/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status, _ = f.do(t, "GET", "/api/groups/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("a deleted group answered %d", status)
	}
}

// The patch of a file several members change comes from the member whose
// patch holds the cited hunk's range.
func TestPatchPicksTheMemberHoldingTheHunk(t *testing.T) {
	f := newAPI(t)
	g, err := f.store.Create(context.Background(), "acme/demo", "Pair", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	document, _ := json.Marshal(map[string]any{
		"review": map[string]any{"repository": "acme/demo", "pull_request": 7, "version": 1},
		"stack":  map[string]any{"title": "Pair", "members": []ReviewedMember{{Number: 7, HeadSHA: "aaaaaaa", ReviewVersion: 1}, {Number: 8, HeadSHA: "bbbbbbb", ReviewVersion: 1}}},
		"index": map[string]any{
			"hunks": []any{map[string]any{"id": "h7", "path": "a.go", "new_range": []int{1, 3}}, map[string]any{"id": "h8", "path": "a.go", "new_range": []int{12, 3}}},
			"file_comparisons": []any{
				map[string]any{"base_revision": "base7", "changes": []any{map[string]any{"path": "a.go", "change": "modified"}}},
				map[string]any{"base_revision": "base8", "changes": []any{map[string]any{"path": "a.go", "change": "modified"}}},
			},
		},
	})
	version := groupReview(t, "acme/demo", 1)
	version.Document = document
	if err := f.store.Put(context.Background(), g.ID, 0, version); err != nil {
		t.Fatal(err)
	}
	status, raw := f.do(t, "GET", "/api/groups/"+g.ID+"/patch?path=a.go&hunk=h8", "")
	if status != http.StatusOK || !strings.Contains(string(raw), "+12,3") {
		t.Fatalf("patch for h8: %d %s", status, raw)
	}
	last := f.patches[len(f.patches)-1]
	if last.Head != "bbbbbbb" || last.MergeBase != "base8" || last.Dir != "/clone" || last.Repository != "acme/demo" {
		t.Fatalf("read %+v", last)
	}
	if _, raw = f.do(t, "GET", "/api/groups/"+g.ID+"/patch?path=a.go&hunk=h7", ""); !strings.Contains(string(raw), "+1,3") {
		t.Fatalf("patch for h7: %s", raw)
	}
	if status, _ = f.do(t, "GET", "/api/groups/"+g.ID+"/patch?path=b.go", ""); status != http.StatusNotFound {
		t.Fatalf("an uncited path answered %d", status)
	}
}
