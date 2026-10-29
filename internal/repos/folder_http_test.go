package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
)

func TestFolderHTTPRegistersMatchingUpstream(t *testing.T) {
	r := registry(t)
	path := clone(t, "https://github.com/me/fork")
	git(t, path, "remote", "add", "upstream", "https://github.com/owner/repo")
	for _, tc := range []struct {
		name   string
		status int
	}{{"owner/mismatch", 400}, {"owner/repo", 200}} {
		body, _ := json.Marshal(map[string]string{"path": path, "repository": tc.name})
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/repos", strings.NewReader(string(body))))
		if rec.Code != tc.status {
			t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	list, err := r.Repositories(context.Background())
	if err != nil || len(list) != 1 || list[0].Repository != "owner/repo" || len(list[0].Clones) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
}

// postFolder registers path for repository through handler and returns the
// status and the decoded body.
func postFolder(t *testing.T, handler http.Handler, path, repository string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path, "repository": repository})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/repos", strings.NewReader(string(body))))
	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, answer
}

func TestFolderHTTPRegistersARemoteUsingAnEarlierName(t *testing.T) {
	ctx := context.Background()
	// Earlier names are process-wide: each run uses its own.
	old, current := uniqueName("folder-old"), uniqueName("folder-new")
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	path := clone(t, "git@github.com:"+strings.ToUpper(old)+".git")
	// Registered under the earlier name before the rename.
	before := clone(t, "https://github.com/"+old)
	if c, err := r.Add(ctx, before, nil); err != nil || c.Repository != old {
		t.Fatalf("Add = %+v, %v", c, err)
	}
	automatic := true
	if err := r.UpdateRepository(ctx, old, Settings{Automatic: &automatic}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	lookup := func(_ context.Context, repository string) (string, error) {
		asked = append(asked, repository)
		return current, nil
	}
	status, answer := postFolder(t, r.HandlerWith(lookup), path, strings.ToUpper(current))
	if status != 200 || answer["repository"] != current || len(asked) != 1 || asked[0] != old {
		t.Fatalf("%d %v, asked %v", status, answer, asked)
	}
	// Both clones are available clones of the repository, which is picked
	// once, and their remotes name it for every caller matching remotes.
	list, err := r.Repositories(ctx)
	if err != nil || len(list) != 1 || list[0].Repository != current || len(list[0].Clones) != 2 || !list[0].Clones[0].Available || !list[0].Clones[1].Available || list[0].Settings.Automatic == nil || !*list[0].Settings.Automatic || strings.Join(list[0].EarlierNames, ",") != old {
		t.Fatalf("%+v %v", list, err)
	}
	if !gitlocal.SameRepository("https://github.com/"+old, current) || gitlocal.SameRepository("https://gitlab.com/"+old, current) {
		t.Fatal("the earlier name does not read as the current one")
	}
	if gh, err := github.RepositoryFromRemote("https://github.com/" + old + ".git"); err != nil || gh.String() != current {
		t.Fatalf("RepositoryFromRemote = %v, %v", gh, err)
	}
}

func TestOpenLoadsRecordedEarlierNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "registry.sqlite")
	r, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	old, current := uniqueName("load-old"), uniqueName("load-new")
	if _, err = r.db.Exec("INSERT INTO aliases VALUES (?,?)", old, current); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if gitlocal.RenamedTo(old) != old {
		t.Fatal("loaded before Open")
	}
	r, err = OpenReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if got := gitlocal.RenamedTo(strings.ToUpper(old)); got != current {
		t.Fatalf("RenamedTo = %q", got)
	}
}

// A recheck drops the earlier names GitHub now answers with another
// repository, and keeps those it still forwards there, even after another
// rename, and those it could not check or does not show.
func TestRecheckAliasesDropsNamesNoLongerForwarded(t *testing.T) {
	r := registry(t)
	current, renamedAgain := uniqueName("recheck-new"), uniqueName("recheck-newer")
	kept, again, taken, hidden, unchecked := uniqueName("recheck-kept"), uniqueName("recheck-again"), uniqueName("recheck-taken"), uniqueName("recheck-hidden"), uniqueName("recheck-unchecked")
	answers := map[string]string{kept: current, taken: taken, hidden: "", unchecked: ""}
	for _, alias := range []string{kept, taken, hidden, unchecked} {
		if _, err := r.db.Exec("INSERT INTO aliases VALUES (?,?)", alias, current); err != nil {
			t.Fatal(err)
		}
		gitlocal.AddRenamed(alias, current)
	}
	// again was recorded for renamedAgain's earlier name, which GitHub now
	// forwards on.
	if _, err := r.db.Exec("INSERT INTO aliases VALUES (?,?)", again, current+"-was"); err != nil {
		t.Fatal(err)
	}
	gitlocal.AddRenamed(again, current+"-was")
	err := r.RecheckAliases(context.Background(), "", func(_ context.Context, repo github.Repository) (string, error) {
		switch name := repo.String(); name {
		case again, current + "-was":
			return renamedAgain, nil
		case current:
			return current, nil
		case hidden:
			return "", github.ErrNotFound
		case unchecked:
			return "", errors.New("offline")
		default:
			return answers[name], nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{hidden, kept, unchecked}
	slices.Sort(want)
	if got := gitlocal.EarlierNames(current); !slices.Equal(got, want) {
		t.Fatalf("earlier names %v, want %v", got, want)
	}
	var stored int
	if err := r.db.QueryRow("SELECT count(*) FROM aliases").Scan(&stored); err != nil || stored != 4 {
		t.Fatalf("%d aliases stored, %v", stored, err)
	}
	if gitlocal.RenamedTo(taken) != taken || gitlocal.RenamedTo(again) != current+"-was" {
		t.Fatal("the taken name reads as the repository, or the renamed-again one was dropped")
	}
}

func TestFolderHTTPRefusesAnotherRepository(t *testing.T) {
	r := registry(t)
	path := clone(t, "https://github.com/someone/other")
	for name, current := range map[string]CurrentName{
		"another repository": func(context.Context, string) (string, error) { return "someone/other", nil },
		"not visible":        func(context.Context, string) (string, error) { return "", github.ErrNotFound },
		"no lookup":          nil,
	} {
		status, answer := postFolder(t, r.HandlerWith(current), path, "owner/repo")
		if want := "This folder is a clone of someone/other, not owner/repo. Choose a clone of owner/repo."; status != 400 || answer["message"] != want {
			t.Fatalf("%s: %d %v", name, status, answer)
		}
	}
	status, answer := postFolder(t, r.HandlerWith(func(context.Context, string) (string, error) { return "", errors.New("offline") }), path, "owner/repo")
	if status != 400 || !strings.Contains(answer["message"].(string), "could not check with GitHub whether that is an earlier name of owner/repo") {
		t.Fatalf("%d %v", status, answer)
	}
	if list, _ := r.Repositories(context.Background()); len(list) != 0 {
		t.Fatalf("a refused folder registered %+v", list)
	}
	status, answer = postFolder(t, r.Handler(), t.TempDir(), "owner/repo")
	if status != 400 || answer["message"] != "This folder does not exist or is not a git working tree." {
		t.Fatalf("%d %v", status, answer)
	}
	local := clone(t, "https://gitlab.com/owner/repo")
	status, answer = postFolder(t, r.Handler(), local, "owner/repo")
	if status != 400 || answer["message"] != "This folder has no GitHub remote. Choose a clone of owner/repo." {
		t.Fatalf("%d %v", status, answer)
	}
}

// uniqueName is an owner/name no other test or run uses.
func uniqueName(owner string) string {
	return fmt.Sprintf("%s-%d/repo", owner, time.Now().UnixNano())
}
