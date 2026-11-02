package viewserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

type commitAnswer struct {
	OID   string `json:"oid"`
	Body  string `json:"body"`
	Files []struct {
		Path      string `json:"path"`
		OldPath   string `json:"old_path"`
		Status    string `json:"status"`
		Additions *int64 `json:"additions"`
		Deletions *int64 `json:"deletions"`
	} `json:"files"`
	Code string `json:"code"`
}

func fetchCommit(t *testing.T, base, oid string) (*http.Response, commitAnswer) {
	t.Helper()
	resp, body := get(t, base+"/api/commit?oid="+url.QueryEscape(oid))
	var decoded commitAnswer
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("/api/commit: %v\n%s", err, body)
		}
	}
	return resp, decoded
}

func fetchCommitPatch(t *testing.T, base, oid, path string) (*http.Response, *string) {
	t.Helper()
	resp, body := get(t, base+"/api/commit-patch?oid="+url.QueryEscape(oid)+"&path="+url.QueryEscape(path))
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	var decoded struct {
		Patch *string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("/api/commit-patch: %v\n%s", err, body)
	}
	return resp, decoded.Patch
}

func count(n int64) *int64 { return &n }

func TestCommitsAreAnsweredOnlyForLinkedCommitsAndTheirFiles(t *testing.T) {
	l := &linker{}
	var commits, patches atomic.Int32
	var asked []string
	_, base := start(t, Options{
		Document: []byte(reviewDocument(headA, "One")),
		Sessions: l.fn,
		Commit: func(_ context.Context, _ Review, oid string) (locallink.CommitDetail, error) {
			commits.Add(1)
			return locallink.CommitDetail{Available: true, OID: oid, Parent: base0, Body: "Why it changed.\n\n<b>not markup</b>", Files: []locallink.CommitFile{
				{Path: "src/a.go", Status: "modified", Additions: count(3), Deletions: count(1)},
				{Path: "src/new.go", OldPath: "src/old.go", Status: "renamed", Additions: count(0), Deletions: count(0)},
				{Path: "logo.png", Status: "added"},
			}}, nil
		},
		CommitPatch: func(_ context.Context, _ Review, commit locallink.CommitDetail, file locallink.CommitFile) (locallink.PatchResult, error) {
			patches.Add(1)
			asked = append(asked, commit.OID+" "+file.Path)
			if file.Status == "added" {
				return locallink.PatchResult{Code: locallink.CodeNoTextPatch}, nil
			}
			return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-x\n+" + file.Path + "\n"}, nil
		},
	})

	// The linker lists one commit, the head.
	resp, answer := fetchCommit(t, base, headA)
	if resp.StatusCode != http.StatusOK || answer.OID != headA || answer.Body != "Why it changed.\n\n<b>not markup</b>" || len(answer.Files) != 3 {
		t.Fatalf("commit: status %d, %+v", resp.StatusCode, answer)
	}
	if f := answer.Files[0]; f.Path != "src/a.go" || f.Status != "modified" || f.Additions == nil || *f.Additions != 3 || *f.Deletions != 1 {
		t.Errorf("modified file = %+v", f)
	}
	if f := answer.Files[1]; f.OldPath != "src/old.go" || f.Status != "renamed" || f.Additions == nil || *f.Additions != 0 {
		t.Errorf("renamed file = %+v", f)
	}
	if f := answer.Files[2]; f.Additions != nil || f.Deletions != nil {
		t.Errorf("a binary file has no line counts: %+v", f)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", resp.Header)
	}
	fetchCommit(t, base, headA)
	if n := commits.Load(); n != 1 {
		t.Errorf("commit reads = %d, want 1: a commit is kept", n)
	}

	// A commit the sessions answer does not list is 404, whatever it is.
	for _, oid := range []string{headB, base0, "HEAD", "main", headA[:7], headA + "^", "--output=/tmp/x", ""} {
		if resp, _ := fetchCommit(t, base, oid); resp.StatusCode != http.StatusNotFound {
			t.Errorf("commit %q: status %d", oid, resp.StatusCode)
		}
		if resp, _ := fetchCommitPatch(t, base, oid, "src/a.go"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("commit-patch of %q: status %d", oid, resp.StatusCode)
		}
	}

	if _, patch := fetchCommitPatch(t, base, headA, "src/a.go"); patch == nil || *patch != "@@ -1 +1 @@\n-x\n+src/a.go\n" {
		t.Errorf("src/a.go: %v", patch)
	}
	// A renamed file is read under its new name, whichever name is asked.
	if _, patch := fetchCommitPatch(t, base, headA, "src/old.go"); patch == nil || *patch != "@@ -1 +1 @@\n-x\n+src/new.go\n" {
		t.Errorf("src/old.go: %v", patch)
	}
	if resp, patch := fetchCommitPatch(t, base, headA, "logo.png"); resp.StatusCode != http.StatusOK || patch != nil {
		t.Errorf("logo.png: status %d, patch %v; want 200 and null", resp.StatusCode, patch)
	}
	// Only the commit's own files are read.
	for _, path := range []string{"src/secret.go", "../src/a.go", "src", "/src/a.go", ""} {
		if resp, _ := fetchCommitPatch(t, base, headA, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("path %q: status %d", path, resp.StatusCode)
		}
	}
	fetchCommitPatch(t, base, headA, "src/a.go")
	fetchCommitPatch(t, base, headA, "src/new.go")
	if n := patches.Load(); n != 3 {
		t.Errorf("patch reads = %d (%v), want one per file", n, asked)
	}
	if n := commits.Load(); n != 1 {
		t.Errorf("commit reads = %d, want the commit read once", n)
	}
}

func TestCommitsAreNotFoundWhileSessionsAreUnavailable(t *testing.T) {
	l := &linker{reply: func(int) (locallink.Result, error) {
		return locallink.Unavailable(locallink.CodeCommitsMissing, "missing"), nil
	}}
	called := false
	_, base := start(t, Options{
		Document: []byte(reviewDocument(headA, "One")),
		Sessions: l.fn,
		Commit: func(context.Context, Review, string) (locallink.CommitDetail, error) {
			called = true
			return locallink.CommitDetail{}, nil
		},
	})
	if resp, _ := fetchCommit(t, base, headA); resp.StatusCode != http.StatusNotFound || called {
		t.Errorf("status %d, read %v", resp.StatusCode, called)
	}
}

func TestCommitWithoutAFunctionIsUnavailable(t *testing.T) {
	l := &linker{}
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "One")), Sessions: l.fn})
	resp, answer := fetchCommit(t, base, headA)
	if resp.StatusCode != http.StatusOK || answer.Files != nil || answer.Code != locallink.CodeError {
		t.Errorf("status %d, %+v", resp.StatusCode, answer)
	}
	if resp, _ := fetchCommitPatch(t, base, headA, "src/a.go"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a patch of a commit whose files are unknown: status %d", resp.StatusCode)
	}
}

// The whole path, with the real reader: a clone with the pull request's
// commits, a root commit, and a rename.
func TestCommitEndToEndFromAClone(t *testing.T) {
	f := linktest.NewWithTrailer(t, "Refs: #12")
	linktest.Git(t, f.Repo, "checkout", "-q", "feature")
	if err := os.Rename(filepath.Join(f.Repo, "b.txt"), filepath.Join(f.Repo, "d.txt")); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Rename b")
	renamed := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")

	document := fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":%q,"pull_request":7,"head":%q,"base":%q}}`+"\n",
		linktest.Repository, renamed, f.Base)
	linked := []string{f.Base, f.Commits[0], renamed}
	_, base := start(t, Options{
		Document: []byte(document),
		Sessions: func(context.Context, Review) (locallink.Result, error) {
			result := locallink.Result{Available: true}
			for _, oid := range linked {
				result.Commits = append(result.Commits, locallink.Commit{OID: oid, Links: []locallink.Link{}})
			}
			return result, nil
		},
		Commit: func(ctx context.Context, review Review, oid string) (locallink.CommitDetail, error) {
			return locallink.ReadCommit(ctx, locallink.CommitRequest{Dir: f.Repo, Repository: review.Repository, OID: oid})
		},
		CommitPatch: func(ctx context.Context, review Review, commit locallink.CommitDetail, file locallink.CommitFile) (locallink.PatchResult, error) {
			return locallink.CommitPatch(ctx, locallink.CommitPatchRequest{Dir: f.Repo, Repository: review.Repository, Commit: commit, File: file})
		},
	})

	_, first := fetchCommit(t, base, f.Commits[0])
	if first.Body != "Refs: #12" || len(first.Files) != 1 || first.Files[0].Path != "b.txt" || first.Files[0].Status != "added" ||
		first.Files[0].Additions == nil || *first.Files[0].Additions != 1 || *first.Files[0].Deletions != 0 {
		t.Fatalf("first commit = %+v", first)
	}
	if _, patch := fetchCommitPatch(t, base, f.Commits[0], "b.txt"); patch == nil || *patch != "@@ -0,0 +1 @@\n+b\n" {
		t.Errorf("b.txt: %v", patch)
	}
	if resp, _ := fetchCommitPatch(t, base, f.Commits[0], "a.txt"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a.txt, which the commit does not change: status %d", resp.StatusCode)
	}

	// The root commit is diffed against the empty tree.
	_, root := fetchCommit(t, base, f.Base)
	if root.Body != "" || len(root.Files) != 1 || root.Files[0].Path != "a.txt" || root.Files[0].Status != "added" {
		t.Fatalf("root commit = %+v", root)
	}
	if _, patch := fetchCommitPatch(t, base, f.Base, "a.txt"); patch == nil || *patch != "@@ -0,0 +1 @@\n+a\n" {
		t.Errorf("root a.txt: %v", patch)
	}

	_, moved := fetchCommit(t, base, renamed)
	if len(moved.Files) != 1 || moved.Files[0].Path != "d.txt" || moved.Files[0].OldPath != "b.txt" || moved.Files[0].Status != "renamed" {
		t.Fatalf("renaming commit = %+v", moved)
	}
	// A rename without a change in content has no text patch.
	if resp, patch := fetchCommitPatch(t, base, renamed, "b.txt"); resp.StatusCode != http.StatusOK || patch != nil {
		t.Errorf("renamed b.txt: status %d, patch %v", resp.StatusCode, patch)
	}

	// The second commit is in the clone, but not linked.
	if resp, _ := fetchCommit(t, base, f.Commits[1]); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unlinked commit: status %d", resp.StatusCode)
	}
}
